package app_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/app"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/apperr"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/event"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/money"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/wagering"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/wallet"
)

type memUoW struct {
	mu            sync.Mutex
	wallets       map[uuid.UUID]wallet.Wallet
	txs           map[uuid.UUID]wagering.WagerTransaction
	byIdem        map[string]uuid.UUID
	byExt         map[string]uuid.UUID
	ledger        []wallet.LedgerEntry
	outbox        []event.Envelope
	inbox         map[string]string
	refRetry      map[uuid.UUID]app.ReferenceRetry
	reversalByRef map[uuid.UUID]uuid.UUID
}

func newMemUoW() *memUoW {
	return &memUoW{
		wallets:       map[uuid.UUID]wallet.Wallet{},
		txs:           map[uuid.UUID]wagering.WagerTransaction{},
		byIdem:        map[string]uuid.UUID{},
		byExt:         map[string]uuid.UUID{},
		inbox:         map[string]string{},
		refRetry:      map[uuid.UUID]app.ReferenceRetry{},
		reversalByRef: map[uuid.UUID]uuid.UUID{},
	}
}

func (u *memUoW) WithinTransaction(ctx context.Context, fn func(ctx context.Context, repos app.Repositories) error) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	return fn(ctx, &memRepos{u: u})
}

func (u *memUoW) WithinRepeatableRead(ctx context.Context, fn func(ctx context.Context, repos app.Repositories) error) error {
	return u.WithinTransaction(ctx, fn)
}

type memRepos struct{ u *memUoW }

func (r *memRepos) Wallets() app.WalletRepository                  { return &memWallets{u: r.u} }
func (r *memRepos) Transactions() app.TransactionRepository        { return &memTxs{u: r.u} }
func (r *memRepos) Ledger() app.LedgerRepository                   { return &memLedger{u: r.u} }
func (r *memRepos) Inbox() app.InboxRepository                     { return &memInbox{u: r.u} }
func (r *memRepos) Outbox() app.OutboxRepository                   { return &memOutbox{u: r.u} }
func (r *memRepos) ReferenceRetries() app.ReferenceRetryRepository { return &memRef{u: r.u} }

type memWallets struct{ u *memUoW }

func (m *memWallets) Insert(_ context.Context, w wallet.Wallet) error {
	m.u.wallets[w.ID()] = w
	return nil
}
func (m *memWallets) Get(_ context.Context, id uuid.UUID) (wallet.Wallet, error) {
	w, ok := m.u.wallets[id]
	if !ok {
		return wallet.Wallet{}, apperr.NewFailure(apperr.CodeWalletNotFound, "wallet not found")
	}
	return w, nil
}
func (m *memWallets) GetForUpdate(ctx context.Context, id uuid.UUID) (wallet.Wallet, error) {
	return m.Get(ctx, id)
}
func (m *memWallets) Update(_ context.Context, w wallet.Wallet, expectedVersion int64) error {
	cur, ok := m.u.wallets[w.ID()]
	if !ok || cur.Version() != expectedVersion {
		return apperr.WrapFailure(apperr.CodeConflict, "version conflict", apperr.ErrConflict)
	}
	m.u.wallets[w.ID()] = w
	return nil
}

type memTxs struct{ u *memUoW }

func idemKey(provider, key string) string { return provider + "|" + key }
func extKey(provider, ext string) string  { return provider + "|" + ext }

func (m *memTxs) Insert(_ context.Context, tx wagering.WagerTransaction) error {
	m.u.txs[tx.ID()] = tx
	if tx.Origin() == wagering.OriginExternal {
		m.u.byIdem[idemKey(tx.ProviderID(), tx.IdempotencyKey())] = tx.ID()
		m.u.byExt[extKey(tx.ProviderID(), tx.ExternalTransactionID())] = tx.ID()
	}
	return nil
}
func (m *memTxs) Update(_ context.Context, tx wagering.WagerTransaction) error {
	m.u.txs[tx.ID()] = tx
	if tx.Status() == wagering.StatusProcessed && tx.ReferenceTransactionID() != nil &&
		(tx.Kind() == wagering.KindRefund || tx.Kind() == wagering.KindRollback) {
		m.u.reversalByRef[*tx.ReferenceTransactionID()] = tx.ID()
	}
	return nil
}
func (m *memTxs) GetByID(_ context.Context, id uuid.UUID) (wagering.WagerTransaction, error) {
	tx, ok := m.u.txs[id]
	if !ok {
		return wagering.WagerTransaction{}, apperr.WrapFailure(apperr.CodeInvalidInput, "not found", apperr.ErrNotFound)
	}
	return tx, nil
}
func (m *memTxs) GetByIdempotencyKey(_ context.Context, providerID, key string) (wagering.WagerTransaction, bool, error) {
	id, ok := m.u.byIdem[idemKey(providerID, key)]
	if !ok {
		return wagering.WagerTransaction{}, false, nil
	}
	return m.u.txs[id], true, nil
}
func (m *memTxs) GetByExternalID(_ context.Context, providerID, externalID string) (wagering.WagerTransaction, bool, error) {
	id, ok := m.u.byExt[extKey(providerID, externalID)]
	if !ok {
		return wagering.WagerTransaction{}, false, nil
	}
	return m.u.txs[id], true, nil
}
func (m *memTxs) TryInsertIdempotency(ctx context.Context, tx wagering.WagerTransaction) (bool, wagering.WagerTransaction, error) {
	if id, ok := m.u.byIdem[idemKey(tx.ProviderID(), tx.IdempotencyKey())]; ok {
		return false, m.u.txs[id], nil
	}
	if err := m.Insert(ctx, tx); err != nil {
		return false, wagering.WagerTransaction{}, err
	}
	return true, tx, nil
}
func (m *memTxs) GetProcessedReversalByReference(_ context.Context, referenceID uuid.UUID) (wagering.WagerTransaction, bool, error) {
	id, ok := m.u.reversalByRef[referenceID]
	if !ok {
		return wagering.WagerTransaction{}, false, nil
	}
	return m.u.txs[id], true, nil
}
func (m *memTxs) ListPendingReferencesDue(_ context.Context, now time.Time, limit int) ([]wagering.WagerTransaction, error) {
	var out []wagering.WagerTransaction
	for id, retry := range m.u.refRetry {
		if retry.NextAttemptAt.After(now) {
			continue
		}
		tx := m.u.txs[id]
		if tx.Status() == wagering.StatusPendingReference {
			out = append(out, tx)
			if len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

type memLedger struct{ u *memUoW }

func (m *memLedger) Insert(_ context.Context, entry wallet.LedgerEntry) error {
	m.u.ledger = append(m.u.ledger, entry)
	return nil
}
func (m *memLedger) ListByWallet(_ context.Context, walletID uuid.UUID, _ *time.Time, _ *uuid.UUID, limit int) ([]wallet.LedgerEntry, error) {
	var out []wallet.LedgerEntry
	for _, e := range m.u.ledger {
		if e.WalletID() == walletID {
			out = append(out, e)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

type memInbox struct{ u *memUoW }

func (m *memInbox) Insert(_ context.Context, consumerName, messageID, payloadHash string) (bool, error) {
	k := consumerName + "|" + messageID
	if _, ok := m.u.inbox[k]; ok {
		return false, nil
	}
	m.u.inbox[k] = payloadHash
	return true, nil
}
func (m *memInbox) MarkCompleted(context.Context, string, string) error { return nil }

type memOutbox struct{ u *memUoW }

func (m *memOutbox) Insert(_ context.Context, env event.Envelope) error {
	m.u.outbox = append(m.u.outbox, env)
	return nil
}
func (m *memOutbox) ClaimBatch(context.Context, time.Time, time.Time, int) ([]app.OutboxRecord, error) {
	return nil, nil
}
func (m *memOutbox) MarkPublished(context.Context, uuid.UUID, time.Time) error { return nil }
func (m *memOutbox) MarkPublishFailed(context.Context, uuid.UUID, int, time.Time, bool) error {
	return nil
}

type memRef struct{ u *memUoW }

func (m *memRef) Upsert(_ context.Context, transactionID uuid.UUID, attempts int, nextAttemptAt, expiresAt, updatedAt time.Time) error {
	m.u.refRetry[transactionID] = app.ReferenceRetry{
		TransactionID: transactionID, Attempts: attempts, NextAttemptAt: nextAttemptAt, ExpiresAt: expiresAt, UpdatedAt: updatedAt,
	}
	return nil
}
func (m *memRef) Get(_ context.Context, transactionID uuid.UUID) (app.ReferenceRetry, bool, error) {
	r, ok := m.u.refRetry[transactionID]
	return r, ok, nil
}
func (m *memRef) Delete(_ context.Context, transactionID uuid.UUID) error {
	delete(m.u.refRetry, transactionID)
	return nil
}

func seedWallet(t *testing.T, uow *memUoW, balance string) (uuid.UUID, uuid.UUID) {
	t.Helper()
	player := uuid.Must(uuid.NewV7())
	wid := uuid.Must(uuid.NewV7())
	bal, err := money.Parse(balance, "BRL")
	if err != nil {
		t.Fatal(err)
	}
	w, err := wallet.Create(wid, player, bal, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	uow.wallets[wid] = w
	return player, wid
}

func TestProcessWagerBetAndReplaySnapshot(t *testing.T) {
	uow := newMemUoW()
	player, wid := seedWallet(t, uow, "100.00")
	uc := app.NewProcessWagerTransaction(uow)

	in := app.ProcessWagerInput{
		ProviderID: "provider-a", ExternalTransactionID: "tx-1", IdempotencyKey: "provider-a:tx-1",
		PlayerID: player, WalletID: wid, RoundID: "r1", GameID: "g1",
		Kind: wagering.KindBet, Money: mustMoney(t, "80.00"),
	}
	out, err := uc.Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if out.Transaction.Status() != wagering.StatusProcessed {
		t.Fatalf("status=%s", out.Transaction.Status())
	}
	if out.Balance.String() != "20.00" {
		t.Fatalf("balance=%s", out.Balance.String())
	}

	// Move wallet further so replay must return snapshot 20.00, not current.
	w := uow.wallets[wid]
	mov, err := w.Credit(uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), mustMoney(t, "50.00"), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	uow.wallets[wid] = mov.Wallet

	replay, err := uc.Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !replay.IdempotentReplay {
		t.Fatal("expected replay")
	}
	if replay.Balance.String() != "20.00" {
		t.Fatalf("replay balance=%s want 20.00", replay.Balance.String())
	}
}

func TestProcessWagerConcurrentInsufficientFunds(t *testing.T) {
	uow := newMemUoW()
	player, wid := seedWallet(t, uow, "100.00")
	uc := app.NewProcessWagerTransaction(uow)

	mk := func(ext string) app.ProcessWagerInput {
		return app.ProcessWagerInput{
			ProviderID: "provider-a", ExternalTransactionID: ext, IdempotencyKey: "provider-a:" + ext,
			PlayerID: player, WalletID: wid, RoundID: "r1", GameID: "g1",
			Kind: wagering.KindBet, Money: mustMoney(t, "80.00"),
		}
	}
	a, err := uc.Execute(context.Background(), mk("a"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := uc.Execute(context.Background(), mk("b"))
	if err != nil {
		t.Fatal(err)
	}
	statuses := map[wagering.Status]int{a.Transaction.Status(): 1, b.Transaction.Status(): 1}
	if a.Transaction.Status() == b.Transaction.Status() {
		statuses[a.Transaction.Status()] = 2
	}
	if statuses[wagering.StatusProcessed] != 1 || statuses[wagering.StatusRejected] != 1 {
		t.Fatalf("want one processed one rejected, got %v / %v", a.Transaction.Status(), b.Transaction.Status())
	}
	if uow.wallets[wid].Balance().String() != "20.00" {
		t.Fatalf("balance=%s", uow.wallets[wid].Balance().String())
	}
	if len(uow.ledger) != 1 {
		t.Fatalf("ledger entries=%d", len(uow.ledger))
	}
	if b.Transaction.Status() == wagering.StatusRejected && b.Transaction.FailureCode() != apperr.CodeInsufficientFunds &&
		a.Transaction.FailureCode() != apperr.CodeInsufficientFunds {
		t.Fatal("expected INSUFFICIENT_FUNDS")
	}
}

func TestProcessWagerIdempotencyConflict(t *testing.T) {
	uow := newMemUoW()
	player, wid := seedWallet(t, uow, "100.00")
	uc := app.NewProcessWagerTransaction(uow)
	in := app.ProcessWagerInput{
		ProviderID: "provider-a", ExternalTransactionID: "tx-1", IdempotencyKey: "k1",
		PlayerID: player, WalletID: wid, RoundID: "r1", GameID: "g1",
		Kind: wagering.KindBet, Money: mustMoney(t, "10.00"),
	}
	if _, err := uc.Execute(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	in.Money = mustMoney(t, "11.00")
	_, err := uc.Execute(context.Background(), in)
	if err == nil {
		t.Fatal("expected conflict")
	}
	if f, ok := apperr.AsFailure(err); !ok || f.Code != apperr.CodeConflict {
		t.Fatalf("err=%v", err)
	}
}

func TestProcessWagerPendingReference(t *testing.T) {
	uow := newMemUoW()
	player, wid := seedWallet(t, uow, "100.00")
	uc := app.NewProcessWagerTransaction(uow)
	out, err := uc.Execute(context.Background(), app.ProcessWagerInput{
		ProviderID: "provider-a", ExternalTransactionID: "ref-1", IdempotencyKey: "provider-a:ref-1",
		PlayerID: player, WalletID: wid, RoundID: "r1", GameID: "g1",
		Kind: wagering.KindRefund, Money: mustMoney(t, "10.00"),
		ReferenceExternalTransactionID: "missing-bet",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Transaction.Status() != wagering.StatusPendingReference {
		t.Fatalf("status=%s", out.Transaction.Status())
	}
	if len(uow.refRetry) != 1 {
		t.Fatal("expected reference retry state")
	}
}

func TestProcessWagerRollbackOfProcessedRefund(t *testing.T) {
	uow := newMemUoW()
	player, wid := seedWallet(t, uow, "100.00")
	uc := app.NewProcessWagerTransaction(uow)

	bet, err := uc.Execute(context.Background(), app.ProcessWagerInput{
		ProviderID: "provider-a", ExternalTransactionID: "bet-1", IdempotencyKey: "k-bet-1",
		PlayerID: player, WalletID: wid, RoundID: "r1", GameID: "g1",
		Kind: wagering.KindBet, Money: mustMoney(t, "40.00"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if bet.Balance.String() != "60.00" {
		t.Fatalf("after bet bal=%s", bet.Balance.String())
	}

	refund, err := uc.Execute(context.Background(), app.ProcessWagerInput{
		ProviderID: "provider-a", ExternalTransactionID: "refund-1", IdempotencyKey: "k-refund-1",
		PlayerID: player, WalletID: wid, RoundID: "r1", GameID: "g1",
		Kind: wagering.KindRefund, Money: mustMoney(t, "40.00"),
		ReferenceExternalTransactionID: "bet-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if refund.Transaction.Status() != wagering.StatusProcessed || refund.Balance.String() != "100.00" {
		t.Fatalf("refund status=%s bal=%s", refund.Transaction.Status(), refund.Balance.String())
	}

	rb, err := uc.Execute(context.Background(), app.ProcessWagerInput{
		ProviderID: "provider-a", ExternalTransactionID: "rollback-1", IdempotencyKey: "k-rollback-1",
		PlayerID: player, WalletID: wid, RoundID: "r1", GameID: "g1",
		Kind: wagering.KindRollback, Money: mustMoney(t, "40.00"),
		ReferenceExternalTransactionID: "refund-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if rb.Transaction.Status() != wagering.StatusProcessed {
		t.Fatalf("rollback status=%s code=%s", rb.Transaction.Status(), rb.Transaction.FailureCode())
	}
	if rb.Balance.String() != "60.00" {
		t.Fatalf("after rollback bal=%s want 60.00", rb.Balance.String())
	}
}

func mustMoney(t *testing.T, s string) money.Money {
	t.Helper()
	m, err := money.Parse(s, "BRL")
	if err != nil {
		t.Fatal(err)
	}
	return m
}
