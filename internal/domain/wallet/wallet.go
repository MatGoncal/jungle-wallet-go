package wallet

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/apperr"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/money"
)

type Direction string

const (
	DirectionDebit  Direction = "DEBIT"
	DirectionCredit Direction = "CREDIT"
)

// LedgerEntry is an immutable wallet ledger line.
type LedgerEntry struct {
	id            uuid.UUID
	walletID      uuid.UUID
	transactionID uuid.UUID
	direction     Direction
	amount        money.Money
	balanceBefore money.Money
	balanceAfter  money.Money
	createdAt     time.Time
}

func NewLedgerEntry(
	id uuid.UUID,
	walletID uuid.UUID,
	transactionID uuid.UUID,
	direction Direction,
	amount money.Money,
	balanceBefore money.Money,
	balanceAfter money.Money,
	createdAt time.Time,
) (LedgerEntry, error) {
	if id == uuid.Nil || walletID == uuid.Nil || transactionID == uuid.Nil {
		return LedgerEntry{}, apperr.WrapFailure(apperr.CodeInvalidInput, "ledger ids required", apperr.ErrInvalidArgument)
	}
	if direction != DirectionDebit && direction != DirectionCredit {
		return LedgerEntry{}, apperr.WrapFailure(apperr.CodeInvalidInput, "invalid ledger direction", apperr.ErrInvalidArgument)
	}
	if !amount.IsPositive() {
		return LedgerEntry{}, apperr.NewFailure(apperr.CodeInvalidAmount, "ledger amount must be positive")
	}
	if amount.Currency() != balanceBefore.Currency() || amount.Currency() != balanceAfter.Currency() {
		return LedgerEntry{}, apperr.NewFailure(apperr.CodeCurrencyMismatch, "ledger currencies must match")
	}

	var expected money.Money
	var err error
	switch direction {
	case DirectionCredit:
		expected, err = balanceBefore.Add(amount)
	case DirectionDebit:
		expected, err = balanceBefore.Sub(amount)
	}
	if err != nil {
		return LedgerEntry{}, err
	}
	if !expected.Equal(balanceAfter) {
		return LedgerEntry{}, apperr.WrapFailure(
			apperr.CodeInvalidInput,
			fmt.Sprintf("balanceAfter %s != expected %s", balanceAfter.String(), expected.String()),
			apperr.ErrInvariantViolation,
		)
	}
	if balanceAfter.IsNegative() {
		return LedgerEntry{}, apperr.NewFailure(apperr.CodeInsufficientFunds, "balance after entry would be negative")
	}

	return LedgerEntry{
		id:            id,
		walletID:      walletID,
		transactionID: transactionID,
		direction:     direction,
		amount:        amount,
		balanceBefore: balanceBefore,
		balanceAfter:  balanceAfter,
		createdAt:     createdAt.UTC(),
	}, nil
}

func (e LedgerEntry) ID() uuid.UUID              { return e.id }
func (e LedgerEntry) WalletID() uuid.UUID        { return e.walletID }
func (e LedgerEntry) TransactionID() uuid.UUID   { return e.transactionID }
func (e LedgerEntry) Direction() Direction       { return e.direction }
func (e LedgerEntry) Amount() money.Money        { return e.amount }
func (e LedgerEntry) BalanceBefore() money.Money { return e.balanceBefore }
func (e LedgerEntry) BalanceAfter() money.Money  { return e.balanceAfter }
func (e LedgerEntry) CreatedAt() time.Time       { return e.createdAt }

// Wallet is the financial aggregate root.
type Wallet struct {
	id        uuid.UUID
	playerID  uuid.UUID
	balance   money.Money
	version   int64
	createdAt time.Time
	updatedAt time.Time
}

type MovementResult struct {
	Wallet Wallet
	Entry  LedgerEntry
}

// Create opens a new wallet. Version starts at 1. Does not emit ledger/events — that is the use case's job.
func Create(id, playerID uuid.UUID, initial money.Money, now time.Time) (Wallet, error) {
	if id == uuid.Nil || playerID == uuid.Nil {
		return Wallet{}, apperr.WrapFailure(apperr.CodeInvalidInput, "wallet and player ids required", apperr.ErrInvalidArgument)
	}
	if initial.Currency() == "" {
		return Wallet{}, apperr.WrapFailure(apperr.CodeInvalidInput, "currency required", apperr.ErrInvalidArgument)
	}
	if initial.IsNegative() {
		return Wallet{}, apperr.NewFailure(apperr.CodeInvalidAmount, "initial balance cannot be negative")
	}
	now = now.UTC()
	return Wallet{
		id:        id,
		playerID:  playerID,
		balance:   initial,
		version:   1,
		createdAt: now,
		updatedAt: now,
	}, nil
}

// Rehydrate rebuilds a wallet from persistence without reapplying movements or emitting events.
func Rehydrate(id, playerID uuid.UUID, balance money.Money, version int64, createdAt, updatedAt time.Time) (Wallet, error) {
	if id == uuid.Nil || playerID == uuid.Nil {
		return Wallet{}, apperr.WrapFailure(apperr.CodeInvalidInput, "wallet and player ids required", apperr.ErrInvalidArgument)
	}
	if version < 1 {
		return Wallet{}, apperr.WrapFailure(apperr.CodeInvalidInput, "version must be >= 1", apperr.ErrInvalidArgument)
	}
	if balance.IsNegative() {
		return Wallet{}, apperr.NewFailure(apperr.CodeInsufficientFunds, "persisted balance cannot be negative")
	}
	return Wallet{
		id:        id,
		playerID:  playerID,
		balance:   balance,
		version:   version,
		createdAt: createdAt.UTC(),
		updatedAt: updatedAt.UTC(),
	}, nil
}

func (w Wallet) ID() uuid.UUID        { return w.id }
func (w Wallet) PlayerID() uuid.UUID  { return w.playerID }
func (w Wallet) Balance() money.Money { return w.balance }
func (w Wallet) Version() int64       { return w.version }
func (w Wallet) CreatedAt() time.Time { return w.createdAt }
func (w Wallet) UpdatedAt() time.Time { return w.updatedAt }
func (w Wallet) Currency() string     { return w.balance.Currency() }

func (w Wallet) Credit(entryID, txID uuid.UUID, amount money.Money, now time.Time) (MovementResult, error) {
	if err := w.requireCurrency(amount); err != nil {
		return MovementResult{}, err
	}
	if !amount.IsPositive() {
		return MovementResult{}, apperr.NewFailure(apperr.CodeInvalidAmount, "credit amount must be positive")
	}
	before := w.balance
	after, err := before.Add(amount)
	if err != nil {
		return MovementResult{}, err
	}
	entry, err := NewLedgerEntry(entryID, w.id, txID, DirectionCredit, amount, before, after, now)
	if err != nil {
		return MovementResult{}, err
	}
	w.balance = after
	w.version++
	w.updatedAt = now.UTC()
	return MovementResult{Wallet: w, Entry: entry}, nil
}

func (w Wallet) Debit(entryID, txID uuid.UUID, amount money.Money, now time.Time) (MovementResult, error) {
	if err := w.requireCurrency(amount); err != nil {
		return MovementResult{}, err
	}
	if !amount.IsPositive() {
		return MovementResult{}, apperr.NewFailure(apperr.CodeInvalidAmount, "debit amount must be positive")
	}
	before := w.balance
	cmp, err := before.Cmp(amount)
	if err != nil {
		return MovementResult{}, err
	}
	if cmp < 0 {
		return MovementResult{}, apperr.NewFailure(apperr.CodeInsufficientFunds, "insufficient funds for debit")
	}
	after, err := before.Sub(amount)
	if err != nil {
		return MovementResult{}, err
	}
	entry, err := NewLedgerEntry(entryID, w.id, txID, DirectionDebit, amount, before, after, now)
	if err != nil {
		return MovementResult{}, err
	}
	w.balance = after
	w.version++
	w.updatedAt = now.UTC()
	return MovementResult{Wallet: w, Entry: entry}, nil
}

func (w Wallet) requireCurrency(amount money.Money) error {
	if amount.Currency() != w.balance.Currency() {
		return apperr.NewFailure(apperr.CodeCurrencyMismatch, "operation currency differs from wallet")
	}
	return nil
}
