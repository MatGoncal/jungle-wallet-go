package app_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/app"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/money"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/wagering"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/wallet"
)

// flipOnLockRepos re-reads as PROCESSED after the wallet lock — simulates a peer that finished first.
type flipOnLockRepos struct {
	*memRepos
	pendingID uuid.UUID
}

func (r *flipOnLockRepos) Wallets() app.WalletRepository {
	return &flipOnLockWallets{memWallets: memWallets{u: r.u}, pendingID: r.pendingID}
}

type flipOnLockWallets struct {
	memWallets
	pendingID uuid.UUID
}

func (m *flipOnLockWallets) GetForUpdate(ctx context.Context, id uuid.UUID) (wallet.Wallet, error) {
	tx := m.u.txs[m.pendingID]
	if tx.Status() == wagering.StatusPendingReference {
		bal, _ := money.FromMinor(0, "BRL")
		done, err := tx.MarkProcessed(bal, nil, time.Now().UTC())
		if err != nil {
			return wallet.Wallet{}, err
		}
		m.u.txs[m.pendingID] = done
	}
	return m.memWallets.GetForUpdate(ctx, id)
}

type flipOnLockUoW struct {
	inner     *memUoW
	pendingID uuid.UUID
}

func (u *flipOnLockUoW) WithinTransaction(ctx context.Context, fn func(ctx context.Context, repos app.Repositories) error) error {
	u.inner.mu.Lock()
	defer u.inner.mu.Unlock()
	return fn(ctx, &flipOnLockRepos{memRepos: &memRepos{u: u.inner}, pendingID: u.pendingID})
}

func (u *flipOnLockUoW) WithinRepeatableRead(ctx context.Context, fn func(ctx context.Context, repos app.Repositories) error) error {
	return u.WithinTransaction(ctx, fn)
}

func TestResolvePending_ExitsAfterWalletLockIfAlreadyResolved(t *testing.T) {
	inner := newMemUoW()
	player, wid := seedWallet(t, inner, "100.00")
	process := app.NewProcessWagerTransaction(inner)

	bet, err := process.Execute(context.Background(), app.ProcessWagerInput{
		ProviderID: "provider-a", ExternalTransactionID: "bet-1", IdempotencyKey: "k-bet-1",
		PlayerID: player, WalletID: wid, RoundID: "r1", GameID: "g1",
		Kind: wagering.KindBet, Money: mustMoney(t, "40.00"),
	})
	if err != nil {
		t.Fatal(err)
	}

	pending, err := process.Execute(context.Background(), app.ProcessWagerInput{
		ProviderID: "provider-a", ExternalTransactionID: "refund-1", IdempotencyKey: "k-refund-1",
		PlayerID: player, WalletID: wid, RoundID: "r1", GameID: "g1",
		Kind: wagering.KindRefund, Money: mustMoney(t, "40.00"),
		ReferenceExternalTransactionID: "bet-1-late",
	})
	if err != nil {
		t.Fatal(err)
	}
	if pending.Transaction.Status() != wagering.StatusPendingReference {
		t.Fatalf("status=%s", pending.Transaction.Status())
	}

	// Point the pending refund at the processed bet so resolve would credit without the recheck.
	inner.byExt["provider-a|bet-1-late"] = bet.Transaction.ID()
	ledgerBefore := len(inner.ledger)
	balanceBefore := inner.wallets[wid].Balance().String()

	uow := &flipOnLockUoW{inner: inner, pendingID: pending.Transaction.ID()}
	resolve := app.NewResolvePendingReference(uow)
	if err := resolve.Execute(context.Background(), app.ResolvePendingInput{
		TransactionID: pending.Transaction.ID(),
		CorrelationID: "test",
	}); err != nil {
		t.Fatal(err)
	}

	if len(inner.ledger) != ledgerBefore {
		t.Fatalf("ledger grew by %d; recheck should skip credit", len(inner.ledger)-ledgerBefore)
	}
	if inner.wallets[wid].Balance().String() != balanceBefore {
		t.Fatalf("balance=%s want %s", inner.wallets[wid].Balance().String(), balanceBefore)
	}
	if inner.txs[pending.Transaction.ID()].Status() != wagering.StatusProcessed {
		t.Fatalf("status=%s want PROCESSED (flipped by peer)", inner.txs[pending.Transaction.ID()].Status())
	}
}

func TestMemInbox_InsertReturnsExistingHash(t *testing.T) {
	uow := newMemUoW()
	inbox := &memInbox{u: uow}
	ok, _, err := inbox.Insert(context.Background(), "c", "m1", "hash-a")
	if err != nil || !ok {
		t.Fatalf("first insert ok=%v err=%v", ok, err)
	}
	ok, existing, err := inbox.Insert(context.Background(), "c", "m1", "hash-b")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected conflict")
	}
	if existing != "hash-a" {
		t.Fatalf("existing=%q want hash-a", existing)
	}
}
