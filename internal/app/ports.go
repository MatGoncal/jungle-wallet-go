package app

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/event"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/wagering"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/wallet"
)

// UnitOfWork delimits one SQL transaction shared by all repositories.
// Repositories never open transactions themselves.
type UnitOfWork interface {
	WithinTransaction(ctx context.Context, fn func(ctx context.Context, repos Repositories) error) error
	WithinRepeatableRead(ctx context.Context, fn func(ctx context.Context, repos Repositories) error) error
}

// Repositories are bound to the active transaction (or pool when outside UoW helpers).
type Repositories interface {
	Wallets() WalletRepository
	Transactions() TransactionRepository
	Ledger() LedgerRepository
	Inbox() InboxRepository
	Outbox() OutboxRepository
	ReferenceRetries() ReferenceRetryRepository
}

type WalletRepository interface {
	Insert(ctx context.Context, w wallet.Wallet) error
	GetForUpdate(ctx context.Context, id uuid.UUID) (wallet.Wallet, error)
	Get(ctx context.Context, id uuid.UUID) (wallet.Wallet, error)
	Update(ctx context.Context, w wallet.Wallet, expectedVersion int64) error
}

type TransactionRepository interface {
	Insert(ctx context.Context, tx wagering.WagerTransaction) error
	Update(ctx context.Context, tx wagering.WagerTransaction) error
	GetByID(ctx context.Context, id uuid.UUID) (wagering.WagerTransaction, error)
	GetByIdempotencyKey(ctx context.Context, providerID, key string) (wagering.WagerTransaction, bool, error)
	GetByExternalID(ctx context.Context, providerID, externalID string) (wagering.WagerTransaction, bool, error)
	TryInsertIdempotency(ctx context.Context, tx wagering.WagerTransaction) (inserted bool, existing wagering.WagerTransaction, err error)
	GetProcessedReversalByReference(ctx context.Context, referenceID uuid.UUID) (wagering.WagerTransaction, bool, error)
	ListPendingReferencesDue(ctx context.Context, now time.Time, limit int) ([]wagering.WagerTransaction, error)
}

type LedgerRepository interface {
	Insert(ctx context.Context, entry wallet.LedgerEntry) error
	ListByWallet(ctx context.Context, walletID uuid.UUID, afterCreatedAt *time.Time, afterID *uuid.UUID, limit int) ([]wallet.LedgerEntry, error)
	// SumByWallet returns net balance in minor units (credits − debits) and entry count.
	SumByWallet(ctx context.Context, walletID uuid.UUID) (netMinor int64, entryCount int, err error)
}

type InboxRepository interface {
	// Insert records the message. On conflict returns inserted=false and the payload hash already stored.
	Insert(ctx context.Context, consumerName, messageID, payloadHash string) (inserted bool, existingHash string, err error)
	MarkCompleted(ctx context.Context, consumerName, messageID string) error
}

type OutboxRepository interface {
	Insert(ctx context.Context, env event.Envelope) error
	ClaimBatch(ctx context.Context, now time.Time, lockUntil time.Time, limit int) ([]OutboxRecord, error)
	MarkPublished(ctx context.Context, id uuid.UUID, publishedAt time.Time) error
	MarkPublishFailed(ctx context.Context, id uuid.UUID, attempts int, nextAttemptAt time.Time, clearLock bool) error
}

// OutboxRecord is a claimed unpublished event row.
type OutboxRecord struct {
	ID            uuid.UUID
	AggregateID   uuid.UUID
	EventType     string
	Payload       []byte
	CorrelationID string
	Attempts      int
	OccurredAt    time.Time
}

type ReferenceRetryRepository interface {
	Upsert(ctx context.Context, transactionID uuid.UUID, attempts int, nextAttemptAt, expiresAt, updatedAt time.Time) error
	Get(ctx context.Context, transactionID uuid.UUID) (ReferenceRetry, bool, error)
	Delete(ctx context.Context, transactionID uuid.UUID) error
}

type ReferenceRetry struct {
	TransactionID uuid.UUID
	Attempts      int
	NextAttemptAt time.Time
	ExpiresAt     time.Time
	UpdatedAt     time.Time
}
