package app

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/apperr"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/money"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/wagering"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/wallet"
)

type GetWallet struct {
	uow UnitOfWork
}

func NewGetWallet(uow UnitOfWork) *GetWallet { return &GetWallet{uow: uow} }

func (uc *GetWallet) Execute(ctx context.Context, id uuid.UUID) (wallet.Wallet, error) {
	var w wallet.Wallet
	err := uc.uow.WithinTransaction(ctx, func(ctx context.Context, repos Repositories) error {
		var err error
		w, err = repos.Wallets().Get(ctx, id)
		return err
	})
	return w, err
}

type GetTransaction struct {
	uow UnitOfWork
}

func NewGetTransaction(uow UnitOfWork) *GetTransaction { return &GetTransaction{uow: uow} }

func (uc *GetTransaction) ByID(ctx context.Context, id uuid.UUID) (wagering.WagerTransaction, error) {
	var tx wagering.WagerTransaction
	err := uc.uow.WithinTransaction(ctx, func(ctx context.Context, repos Repositories) error {
		var err error
		tx, err = repos.Transactions().GetByID(ctx, id)
		return err
	})
	return tx, err
}

func (uc *GetTransaction) ByExternal(ctx context.Context, providerID, externalID string) (wagering.WagerTransaction, error) {
	var tx wagering.WagerTransaction
	err := uc.uow.WithinTransaction(ctx, func(ctx context.Context, repos Repositories) error {
		found, ok, err := repos.Transactions().GetByExternalID(ctx, providerID, externalID)
		if err != nil {
			return err
		}
		if !ok {
			return apperr.WrapFailure(apperr.CodeInvalidInput, "transaction not found", apperr.ErrNotFound)
		}
		tx = found
		return nil
	})
	return tx, err
}

type ListLedgerInput struct {
	WalletID uuid.UUID
	Cursor   string
	Limit    int
}

type ListLedgerResult struct {
	Entries    []wallet.LedgerEntry
	NextCursor string
}

type ListLedger struct {
	uow UnitOfWork
}

func NewListLedger(uow UnitOfWork) *ListLedger { return &ListLedger{uow: uow} }

func (uc *ListLedger) Execute(ctx context.Context, in ListLedgerInput) (ListLedgerResult, error) {
	limit := in.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	var afterCreated *time.Time
	var afterID *uuid.UUID
	if in.Cursor != "" {
		t, id, err := DecodeLedgerCursor(in.Cursor)
		if err != nil {
			return ListLedgerResult{}, apperr.WrapFailure(apperr.CodeInvalidInput, "invalid cursor", apperr.ErrInvalidArgument)
		}
		afterCreated = &t
		afterID = &id
	}

	var entries []wallet.LedgerEntry
	err := uc.uow.WithinTransaction(ctx, func(ctx context.Context, repos Repositories) error {
		if _, err := repos.Wallets().Get(ctx, in.WalletID); err != nil {
			return err
		}
		var err error
		entries, err = repos.Ledger().ListByWallet(ctx, in.WalletID, afterCreated, afterID, limit+1)
		return err
	})
	if err != nil {
		return ListLedgerResult{}, err
	}

	var next string
	if len(entries) > limit {
		last := entries[limit-1]
		next = EncodeLedgerCursor(last.CreatedAt(), last.ID())
		entries = entries[:limit]
	}
	return ListLedgerResult{Entries: entries, NextCursor: next}, nil
}

// EncodeLedgerCursor builds an opaque cursor from (created_at, id).
func EncodeLedgerCursor(createdAt time.Time, id uuid.UUID) string {
	raw := fmt.Sprintf("%s|%s", createdAt.UTC().Format(time.RFC3339Nano), id.String())
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// DecodeLedgerCursor parses an opaque ledger cursor.
func DecodeLedgerCursor(cursor string) (time.Time, uuid.UUID, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, uuid.Nil, err
	}
	parts := strings.SplitN(string(raw), "|", 2)
	if len(parts) != 2 {
		return time.Time{}, uuid.Nil, fmt.Errorf("bad cursor shape")
	}
	t, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return time.Time{}, uuid.Nil, err
	}
	id, err := uuid.Parse(parts[1])
	if err != nil {
		return time.Time{}, uuid.Nil, err
	}
	return t, id, nil
}

type ReconcileWallet struct {
	uow UnitOfWork
}

func NewReconcileWallet(uow UnitOfWork) *ReconcileWallet { return &ReconcileWallet{uow: uow} }

type ReconcileResult struct {
	WalletID          uuid.UUID
	StoredBalance     money.Money
	CalculatedBalance money.Money
	Difference        money.Money
	Consistent        bool
	CheckedEntries    int
}

func (uc *ReconcileWallet) Execute(ctx context.Context, walletID uuid.UUID) (ReconcileResult, error) {
	var out ReconcileResult
	err := uc.uow.WithinRepeatableRead(ctx, func(ctx context.Context, repos Repositories) error {
		w, err := repos.Wallets().Get(ctx, walletID)
		if err != nil {
			return err
		}
		netMinor, entryCount, err := repos.Ledger().SumByWallet(ctx, walletID)
		if err != nil {
			return err
		}
		calc, err := money.FromMinor(netMinor, w.Currency())
		if err != nil {
			return err
		}
		diffMinor := w.Balance().AmountMinor() - calc.AmountMinor()
		diff, err := money.FromMinor(diffMinor, w.Currency())
		if err != nil {
			return err
		}
		out = ReconcileResult{
			WalletID:          walletID,
			StoredBalance:     w.Balance(),
			CalculatedBalance: calc,
			Difference:        diff,
			Consistent:        diff.IsZero(),
			CheckedEntries:    entryCount,
		}
		return nil
	})
	return out, err
}
