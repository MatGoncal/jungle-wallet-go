package event

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/apperr"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/money"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/wagering"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/wallet"
)

func newUUID(t *testing.T) uuid.UUID {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.New()
	}
	return id
}

func sampleTx(t *testing.T) wagering.WagerTransaction {
	t.Helper()
	amt, _ := money.Parse("10", "BRL")
	p := wagering.NewExternalParams{
		ID:                    newUUID(t),
		ProviderID:            "p",
		ExternalTransactionID: "e",
		IdempotencyKey:        "k",
		PayloadHash:           "h",
		WalletID:              newUUID(t),
		PlayerID:              newUUID(t),
		RoundID:               "r",
		GameID:                "g",
		Kind:                  wagering.KindBet,
		Amount:                amt,
		Now:                   time.Now().UTC(),
	}
	tx, err := wagering.NewExternal(p)
	if err != nil {
		t.Fatal(err)
	}
	return tx
}

func TestConstructorsEventTypeAndVersion(t *testing.T) {
	eventID := newUUID(t)
	tx := sampleTx(t)
	bal, _ := money.Parse("100", "BRL")
	at := time.Date(2025, 5, 1, 0, 0, 0, 0, time.UTC)
	corr := "corr-1"

	env, err := NewWagerTransactionProcessed(eventID, tx, bal, corr, nil, at)
	if err != nil {
		t.Fatal(err)
	}
	if env.EventType != TypeWagerTransactionProcessed || env.Version != 1 {
		t.Fatalf("processed: type=%s version=%d", env.EventType, env.Version)
	}

	env, err = NewWagerTransactionRejected(eventID, tx, corr, nil, at)
	if err != nil {
		t.Fatal(err)
	}
	if env.EventType != TypeWagerTransactionRejected || env.Version != 1 {
		t.Fatalf("rejected: type=%s version=%d", env.EventType, env.Version)
	}

	env, err = NewWagerTransactionPendingReference(eventID, tx, corr, nil, at)
	if err != nil {
		t.Fatal(err)
	}
	if env.EventType != TypeWagerTransactionPendingReference || env.Version != 1 {
		t.Fatalf("pending ref: type=%s version=%d", env.EventType, env.Version)
	}

	wid := newUUID(t)
	pid := newUUID(t)
	zero, _ := money.Parse("0", "BRL")
	w, err := wallet.Create(wid, pid, zero, at)
	if err != nil {
		t.Fatal(err)
	}
	amt, _ := money.Parse("5", "BRL")
	res, err := w.Credit(newUUID(t), tx.ID(), amt, at)
	if err != nil {
		t.Fatal(err)
	}
	env, err = NewWalletBalanceChanged(eventID, res.Entry, res.Wallet.Version(), corr, nil, at)
	if err != nil {
		t.Fatal(err)
	}
	if env.EventType != TypeWalletBalanceChanged || env.Version != 1 {
		t.Fatalf("balance changed: type=%s version=%d", env.EventType, env.Version)
	}
}

func TestConstructorsRejectNilEventID(t *testing.T) {
	tx := sampleTx(t)
	bal, _ := money.Parse("0", "BRL")
	at := time.Now().UTC()

	tests := []struct {
		name string
		fn   func(uuid.UUID) (Envelope, error)
	}{
		{"processed", func(id uuid.UUID) (Envelope, error) {
			return NewWagerTransactionProcessed(id, tx, bal, "", nil, at)
		}},
		{"rejected", func(id uuid.UUID) (Envelope, error) {
			return NewWagerTransactionRejected(id, tx, "", nil, at)
		}},
		{"pending reference", func(id uuid.UUID) (Envelope, error) {
			return NewWagerTransactionPendingReference(id, tx, "", nil, at)
		}},
		{"balance changed", func(id uuid.UUID) (Envelope, error) {
			wid := newUUID(t)
			w, _ := wallet.Create(wid, newUUID(t), bal, at)
			amt, _ := money.Parse("1", "BRL")
			res, _ := w.Credit(newUUID(t), tx.ID(), amt, at)
			return NewWalletBalanceChanged(id, res.Entry, res.Wallet.Version(), "", nil, at)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.fn(uuid.Nil)
			if err == nil {
				t.Fatal("expected error")
			}
			if !errors.Is(err, apperr.ErrInvalidArgument) {
				t.Fatalf("got %v", err)
			}
		})
	}
}
