package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/app"
)

type ctxKey struct{}

// UnitOfWork runs work inside a single pgx transaction and injects tx-bound repos.
type UnitOfWork struct {
	pool *pgxpool.Pool
}

func NewUnitOfWork(pool *pgxpool.Pool) *UnitOfWork {
	return &UnitOfWork{pool: pool}
}

func (u *UnitOfWork) WithinTransaction(ctx context.Context, fn func(ctx context.Context, repos app.Repositories) error) error {
	tx, err := u.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	repos := newRepositories(tx)
	ctx = context.WithValue(ctx, ctxKey{}, tx)
	if err := fn(ctx, repos); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

type repositories struct {
	wallets      *WalletRepo
	transactions *TransactionRepo
	ledger       *LedgerRepo
	inbox        *InboxRepo
	outbox       *OutboxRepo
	refRetries   *ReferenceRetryRepo
}

func newRepositories(q querier) *repositories {
	return &repositories{
		wallets:      NewWalletRepo(q),
		transactions: NewTransactionRepo(q),
		ledger:       NewLedgerRepo(q),
		inbox:        NewInboxRepo(q),
		outbox:       NewOutboxRepo(q),
		refRetries:   NewReferenceRetryRepo(q),
	}
}

func (r *repositories) Wallets() app.WalletRepository                  { return r.wallets }
func (r *repositories) Transactions() app.TransactionRepository        { return r.transactions }
func (r *repositories) Ledger() app.LedgerRepository                   { return r.ledger }
func (r *repositories) Inbox() app.InboxRepository                     { return r.inbox }
func (r *repositories) Outbox() app.OutboxRepository                   { return r.outbox }
func (r *repositories) ReferenceRetries() app.ReferenceRetryRepository { return r.refRetries }

// WithinRepeatableRead runs read-only reconciliation under REPEATABLE READ.
func (u *UnitOfWork) WithinRepeatableRead(ctx context.Context, fn func(ctx context.Context, repos app.Repositories) error) error {
	tx, err := u.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return fmt.Errorf("begin repeatable read: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(ctx, newRepositories(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// querier is implemented by *pgxpool.Pool and pgx.Tx.
type querier interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

var _ app.UnitOfWork = (*UnitOfWork)(nil)
