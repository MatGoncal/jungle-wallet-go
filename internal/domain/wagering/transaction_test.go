package wagering

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/apperr"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/money"
)

func newUUID(t *testing.T) uuid.UUID {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.New()
	}
	return id
}

func baseExternalParams(t *testing.T, kind Kind, amount money.Money, ref string) NewExternalParams {
	t.Helper()
	return NewExternalParams{
		ID:                             newUUID(t),
		ProviderID:                     "prov",
		ExternalTransactionID:          "ext-1",
		IdempotencyKey:                 "idem-1",
		PayloadHash:                    "hash",
		WalletID:                       newUUID(t),
		PlayerID:                       newUUID(t),
		RoundID:                        "round-1",
		GameID:                         "game-1",
		Kind:                           kind,
		Amount:                         amount,
		ReferenceExternalTransactionID: ref,
		Now:                            time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC),
	}
}

func mustParse(t *testing.T, s string) money.Money {
	t.Helper()
	m, err := money.Parse(s, "BRL")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestNewExternalRejectsOpening(t *testing.T) {
	p := baseExternalParams(t, KindOpening, mustParse(t, "10"), "")
	_, err := NewExternal(p)
	if err == nil {
		t.Fatal("OPENING should be rejected")
	}
	var f *apperr.Failure
	if !errors.As(err, &f) || f.Code != apperr.CodeKindNotAllowed {
		t.Fatalf("got %v", err)
	}
}

func TestExternalKindAmountRules(t *testing.T) {
	positiveKinds := []Kind{KindBet, KindWin, KindRefund, KindRollback}
	for _, k := range positiveKinds {
		t.Run(string(k)+"_zero", func(t *testing.T) {
			ref := ""
			if k == KindRefund || k == KindRollback {
				ref = "ref-ext"
			}
			p := baseExternalParams(t, k, mustParse(t, "0"), ref)
			_, err := NewExternal(p)
			if err == nil {
				t.Fatal("expected error for zero amount")
			}
		})
	}
	t.Run("LOSS_nonzero", func(t *testing.T) {
		p := baseExternalParams(t, KindLoss, mustParse(t, "0.01"), "")
		_, err := NewExternal(p)
		if err == nil {
			t.Fatal("LOSS must be zero")
		}
	})
	t.Run("LOSS_zero_ok", func(t *testing.T) {
		zero, _ := money.Zero("BRL")
		p := baseExternalParams(t, KindLoss, zero, "")
		tx, err := NewExternal(p)
		if err != nil {
			t.Fatal(err)
		}
		if tx.Status() != StatusPending {
			t.Fatalf("status = %s", tx.Status())
		}
	})
}

func TestRefundRollbackRequireReference(t *testing.T) {
	for _, k := range []Kind{KindRefund, KindRollback} {
		t.Run(string(k), func(t *testing.T) {
			p := baseExternalParams(t, k, mustParse(t, "1"), "")
			_, err := NewExternal(p)
			if err == nil {
				t.Fatal("reference required")
			}
			if !errors.Is(err, apperr.ErrInvalidArgument) {
				t.Fatalf("%v", err)
			}
		})
	}
}

func TestStateMachine(t *testing.T) {
	now := time.Date(2025, 4, 1, 0, 0, 0, 0, time.UTC)
	zero, _ := money.Zero("BRL")
	p := baseExternalParams(t, KindBet, mustParse(t, "10"), "")

	t.Run("PENDING_transitions", func(t *testing.T) {
		bal := mustParse(t, "100")
		cases := []struct {
			name string
			fn   func(WagerTransaction) (WagerTransaction, error)
		}{
			{"PROCESSED", func(tx WagerTransaction) (WagerTransaction, error) {
				return tx.MarkProcessed(bal, nil, now)
			}},
			{"REJECTED", func(tx WagerTransaction) (WagerTransaction, error) {
				return tx.MarkRejected(apperr.CodeInvalidAmount, now)
			}},
			{"PENDING_REFERENCE", func(tx WagerTransaction) (WagerTransaction, error) {
				return tx.MarkPendingReference(now)
			}},
			{"FAILED", func(tx WagerTransaction) (WagerTransaction, error) {
				return tx.MarkFailed(apperr.CodeConflict, now)
			}},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				fresh, _ := NewExternal(p)
				got, err := c.fn(fresh)
				if err != nil {
					t.Fatalf("%s: %v", c.name, err)
				}
				_ = got
			})
		}
	})

	t.Run("PENDING_REFERENCE_to_PROCESSED_REJECTED", func(t *testing.T) {
		tx2, _ := NewExternal(p)
		var err error
		tx2, err = tx2.MarkPendingReference(now)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx2.MarkProcessed(mustParse(t, "50"), nil, now); err != nil {
			t.Fatalf("processed: %v", err)
		}
		tx3, _ := NewExternal(p)
		tx3, _ = tx3.MarkPendingReference(now)
		if _, err := tx3.MarkRejected(apperr.CodeReferenceNotFound, now); err != nil {
			t.Fatalf("rejected: %v", err)
		}
	})

	t.Run("terminal_no_transition", func(t *testing.T) {
		done, _ := NewExternal(p)
		done, _ = done.MarkProcessed(mustParse(t, "10"), nil, now)
		_, rejErr := done.MarkRejected(apperr.CodeConflict, now)
		if rejErr == nil {
			t.Fatal("terminal should not transition")
		}
		if !errors.Is(rejErr, apperr.ErrInvalidTransition) {
			t.Fatalf("got %v", rejErr)
		}
		if _, err := done.MarkPendingReference(now); err == nil {
			t.Fatal("processed -> pending ref")
		}
	})

	_ = zero
}

func idempotencyBase() IdempotencyInput {
	m, _ := money.Parse("25.50", "BRL")
	return IdempotencyInput{
		ProviderID:            "p1",
		ExternalTransactionID: "e1",
		PlayerID:              "player-1",
		WalletID:              "wallet-1",
		RoundID:               "r1",
		GameID:                "g1",
		Kind:                  KindBet,
		Money:                 m,
	}
}

func TestCanonicalHashSameBusinessFields(t *testing.T) {
	a := idempotencyBase()
	b := idempotencyBase()
	ha, err := CanonicalHash(a)
	if err != nil {
		t.Fatal(err)
	}
	hb, err := CanonicalHash(b)
	if err != nil {
		t.Fatal(err)
	}
	if ha != hb {
		t.Fatalf("hashes differ: %s vs %s", ha, hb)
	}
}

func TestCanonicalHashDiffersOnAmount(t *testing.T) {
	a := idempotencyBase()
	b := idempotencyBase()
	b.Money = mustParse(t, "25.51")
	ha, _ := CanonicalHash(a)
	hb, _ := CanonicalHash(b)
	if ha == hb {
		t.Fatal("expected different hashes")
	}
}

func TestCanonicalHashAmountNormalizedToMinor(t *testing.T) {
	base := idempotencyBase()
	m2550a, _ := money.Parse("25.5", "BRL")
	m2550b, _ := money.Parse("25.50", "BRL")
	base.Money = m2550a
	ha, _ := CanonicalHash(base)
	base.Money = m2550b
	hb, _ := CanonicalHash(base)
	if ha != hb {
		t.Fatalf("25.5 and 25.50 should hash same: %s vs %s", ha, hb)
	}
}
