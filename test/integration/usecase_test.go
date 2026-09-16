//go:build integration

package integration

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/app"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/apperr"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/money"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/wagering"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/wallet"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/infra/postgres"
)

func TestOpenWallet_PositiveCreatesOpeningLedgerOutbox(t *testing.T) {
	pool := openPool(t)
	uow := postgres.NewUnitOfWork(pool)
	uc := app.NewOpenWallet(uow)
	player := mustV7(t)
	bal, _ := money.Parse("100.00", "BRL")
	out, err := uc.Execute(context.Background(), app.OpenWalletInput{
		PlayerID: player, InitialBalance: bal, CorrelationID: "open-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	var txCount, ledgerCount, outboxCount int
	_ = pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM wager_transactions WHERE wallet_id=$1 AND kind='OPENING'`, out.Wallet.ID()).Scan(&txCount)
	_ = pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM wallet_ledger_entries WHERE wallet_id=$1`, out.Wallet.ID()).Scan(&ledgerCount)
	_ = pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM outbox_events WHERE correlation_id=$1`, "open-1").Scan(&outboxCount)
	if txCount != 1 || ledgerCount != 1 || outboxCount != 2 {
		t.Fatalf("opening=%d ledger=%d outbox=%d", txCount, ledgerCount, outboxCount)
	}
	if out.Wallet.Version() != 1 {
		t.Fatalf("version=%d", out.Wallet.Version())
	}
}

func TestOpenWallet_ZeroBalanceNoSideEffects(t *testing.T) {
	pool := openPool(t)
	uow := postgres.NewUnitOfWork(pool)
	uc := app.NewOpenWallet(uow)
	player := mustV7(t)
	bal, _ := money.Parse("0", "BRL")
	out, err := uc.Execute(context.Background(), app.OpenWalletInput{PlayerID: player, InitialBalance: bal})
	if err != nil {
		t.Fatal(err)
	}
	var txCount, ledgerCount, outboxCount int
	_ = pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM wager_transactions WHERE wallet_id=$1`, out.Wallet.ID()).Scan(&txCount)
	_ = pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM wallet_ledger_entries WHERE wallet_id=$1`, out.Wallet.ID()).Scan(&ledgerCount)
	_ = pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM outbox_events WHERE aggregate_id=$1`, out.Wallet.ID()).Scan(&outboxCount)
	if txCount != 0 || ledgerCount != 0 || outboxCount != 0 {
		t.Fatalf("expected no financial side effects, tx=%d ledger=%d outbox=%d", txCount, ledgerCount, outboxCount)
	}
}

func TestProcessWager_ReplayReturnsOriginalBalance(t *testing.T) {
	pool := openPool(t)
	uow := postgres.NewUnitOfWork(pool)
	open := app.NewOpenWallet(uow)
	process := app.NewProcessWagerTransaction(uow)
	player := mustV7(t)
	bal, _ := money.Parse("100.00", "BRL")
	wal, err := open.Execute(context.Background(), app.OpenWalletInput{PlayerID: player, InitialBalance: bal})
	if err != nil {
		t.Fatal(err)
	}
	bet, _ := money.Parse("10.00", "BRL")
	in := app.ProcessWagerInput{
		ProviderID: "provider-a", ExternalTransactionID: "ext-replay-1", IdempotencyKey: "idem-replay-1",
		PlayerID: player, WalletID: wal.Wallet.ID(), RoundID: "r1", GameID: "g1",
		Kind: wagering.KindBet, Money: bet, CorrelationID: "c1",
	}
	first, err := process.Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	win, _ := money.Parse("5.00", "BRL")
	_, err = process.Execute(context.Background(), app.ProcessWagerInput{
		ProviderID: "provider-a", ExternalTransactionID: "ext-win-1", IdempotencyKey: "idem-win-1",
		PlayerID: player, WalletID: wal.Wallet.ID(), RoundID: "r1", GameID: "g1",
		Kind: wagering.KindWin, Money: win, CorrelationID: "c2",
	})
	if err != nil {
		t.Fatal(err)
	}
	replay, err := process.Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !replay.IdempotentReplay {
		t.Fatal("expected idempotent replay")
	}
	if replay.Balance.AmountMinor() != first.Balance.AmountMinor() {
		t.Fatalf("replay balance %d != original %d", replay.Balance.AmountMinor(), first.Balance.AmountMinor())
	}
	var debits int
	_ = pool.QueryRow(context.Background(), `
		SELECT COUNT(*) FROM wallet_ledger_entries WHERE wallet_id=$1 AND direction='DEBIT'`, wal.Wallet.ID()).Scan(&debits)
	if debits != 1 {
		t.Fatalf("debits=%d want 1", debits)
	}
}

func TestProcessWager_PayloadConflictAndExternalIDConflict(t *testing.T) {
	pool := openPool(t)
	uow := postgres.NewUnitOfWork(pool)
	open := app.NewOpenWallet(uow)
	process := app.NewProcessWagerTransaction(uow)
	player := mustV7(t)
	bal, _ := money.Parse("100.00", "BRL")
	wal, _ := open.Execute(context.Background(), app.OpenWalletInput{PlayerID: player, InitialBalance: bal})
	bet, _ := money.Parse("10.00", "BRL")
	in := app.ProcessWagerInput{
		ProviderID: "provider-a", ExternalTransactionID: "ext-conf-1", IdempotencyKey: "idem-conf-1",
		PlayerID: player, WalletID: wal.Wallet.ID(), RoundID: "r1", GameID: "g1",
		Kind: wagering.KindBet, Money: bet,
	}
	if _, err := process.Execute(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	in2 := in
	in2.Money, _ = money.Parse("20.00", "BRL")
	_, err := process.Execute(context.Background(), in2)
	if err == nil || !isConflict(err) {
		t.Fatalf("expected payload conflict, got %v", err)
	}
	in3 := in
	in3.ExternalTransactionID = "ext-conf-1"
	in3.IdempotencyKey = "idem-conf-other"
	_, err = process.Execute(context.Background(), in3)
	if err == nil || !isConflict(err) {
		t.Fatalf("expected external id conflict, got %v", err)
	}
}

func isConflict(err error) bool {
	if errors.Is(err, apperr.ErrConflict) {
		return true
	}
	f, ok := apperr.AsFailure(err)
	return ok && f.Code == apperr.CodeConflict
}

func TestLedger_PaginationNoGapOverlap(t *testing.T) {
	pool := openPool(t)
	uow := postgres.NewUnitOfWork(pool)
	open := app.NewOpenWallet(uow)
	process := app.NewProcessWagerTransaction(uow)
	list := app.NewListLedger(uow)
	player := mustV7(t)
	bal, _ := money.Parse("1000.00", "BRL")
	wal, _ := open.Execute(context.Background(), app.OpenWalletInput{PlayerID: player, InitialBalance: bal})
	bet, _ := money.Parse("1.00", "BRL")
	for i := 0; i < 5; i++ {
		_, err := process.Execute(context.Background(), app.ProcessWagerInput{
			ProviderID: "provider-a", ExternalTransactionID: uuid.NewString(), IdempotencyKey: uuid.NewString(),
			PlayerID: player, WalletID: wal.Wallet.ID(), RoundID: "r", GameID: "g",
			Kind: wagering.KindBet, Money: bet,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	page1, err := list.Execute(context.Background(), app.ListLedgerInput{WalletID: wal.Wallet.ID(), Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(page1.Entries) != 3 || page1.NextCursor == "" {
		t.Fatalf("page1 len=%d cursor=%q", len(page1.Entries), page1.NextCursor)
	}
	page2, err := list.Execute(context.Background(), app.ListLedgerInput{
		WalletID: wal.Wallet.ID(), Cursor: page1.NextCursor, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[uuid.UUID]bool{}
	for _, e := range page1.Entries {
		seen[e.ID()] = true
	}
	for _, e := range page2.Entries {
		if seen[e.ID()] {
			t.Fatalf("overlap on %s", e.ID())
		}
		seen[e.ID()] = true
	}
	if len(seen) != 6 { // opening + 5 bets
		t.Fatalf("total unique entries=%d want 6", len(seen))
	}
}

func TestAtomicity_ForcedFailureRollsBack(t *testing.T) {
	pool := openPool(t)
	uow := postgres.NewUnitOfWork(pool)
	player, walletID := mustV7(t), mustV7(t)
	bal, _ := money.FromMinor(5000, "BRL")
	now := time.Now().UTC()
	w, err := wallet.Create(walletID, player, bal, now)
	if err != nil {
		t.Fatal(err)
	}
	err = uow.WithinTransaction(context.Background(), func(ctx context.Context, repos app.Repositories) error {
		if err := repos.Wallets().Insert(ctx, w); err != nil {
			return err
		}
		return fmt.Errorf("force rollback mid-tx")
	})
	if err == nil {
		t.Fatal("expected error")
	}
	var n int
	_ = pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM wallets WHERE id=$1`, walletID).Scan(&n)
	if n != 0 {
		t.Fatal("wallet should not exist after forced failure")
	}
}

func TestReconciliation_RepeatableReadConsistent(t *testing.T) {
	pool := openPool(t)
	uow := postgres.NewUnitOfWork(pool)
	open := app.NewOpenWallet(uow)
	process := app.NewProcessWagerTransaction(uow)
	reconcile := app.NewReconcileWallet(uow)
	player := mustV7(t)
	bal, _ := money.Parse("100.00", "BRL")
	wal, _ := open.Execute(context.Background(), app.OpenWalletInput{PlayerID: player, InitialBalance: bal})
	bet, _ := money.Parse("30.00", "BRL")
	_, _ = process.Execute(context.Background(), app.ProcessWagerInput{
		ProviderID: "provider-a", ExternalTransactionID: "rec-1", IdempotencyKey: "rec-k-1",
		PlayerID: player, WalletID: wal.Wallet.ID(), RoundID: "r", GameID: "g",
		Kind: wagering.KindBet, Money: bet,
	})
	out, err := reconcile.Execute(context.Background(), wal.Wallet.ID())
	if err != nil {
		t.Fatal(err)
	}
	if !out.Consistent || out.StoredBalance.AmountMinor() != 7000 {
		t.Fatalf("consistent=%v stored=%d", out.Consistent, out.StoredBalance.AmountMinor())
	}
}
