package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/apperr"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/money"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/wallet"
)

type WalletRepo struct {
	q querier
}

func NewWalletRepo(q querier) *WalletRepo {
	return &WalletRepo{q: q}
}

func (r *WalletRepo) Insert(ctx context.Context, w wallet.Wallet) error {
	_, err := r.q.Exec(ctx, `
		INSERT INTO wallets (id, player_id, currency, balance_minor, version, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		w.ID(), w.PlayerID(), w.Currency(), w.Balance().AmountMinor(), w.Version(), w.CreatedAt(), w.UpdatedAt(),
	)
	if err != nil {
		if isUniqueViolation(err) {
			return apperr.WrapFailure(apperr.CodeConflict, "wallet already exists for player/currency", apperr.ErrConflict)
		}
		return fmt.Errorf("insert wallet: %w", err)
	}
	return nil
}

func (r *WalletRepo) Get(ctx context.Context, id uuid.UUID) (wallet.Wallet, error) {
	return r.scanWallet(ctx, `
		SELECT id, player_id, currency, balance_minor, version, created_at, updated_at
		FROM wallets WHERE id = $1`, id)
}

func (r *WalletRepo) GetForUpdate(ctx context.Context, id uuid.UUID) (wallet.Wallet, error) {
	return r.scanWallet(ctx, `
		SELECT id, player_id, currency, balance_minor, version, created_at, updated_at
		FROM wallets WHERE id = $1 FOR UPDATE`, id)
}

func (r *WalletRepo) Update(ctx context.Context, w wallet.Wallet, expectedVersion int64) error {
	tag, err := r.q.Exec(ctx, `
		UPDATE wallets
		SET balance_minor = $1, version = $2, updated_at = $3
		WHERE id = $4 AND version = $5`,
		w.Balance().AmountMinor(), w.Version(), w.UpdatedAt(), w.ID(), expectedVersion,
	)
	if err != nil {
		return fmt.Errorf("update wallet: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return apperr.WrapFailure(apperr.CodeConflict, "wallet version conflict", apperr.ErrConflict)
	}
	return nil
}

func (r *WalletRepo) scanWallet(ctx context.Context, sql string, args ...any) (wallet.Wallet, error) {
	var (
		id, playerID          uuid.UUID
		currency              string
		balanceMinor, version int64
		createdAt, updatedAt  time.Time
	)
	err := r.q.QueryRow(ctx, sql, args...).Scan(
		&id, &playerID, &currency, &balanceMinor, &version, &createdAt, &updatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return wallet.Wallet{}, apperr.NewFailure(apperr.CodeWalletNotFound, "wallet not found")
	}
	if err != nil {
		return wallet.Wallet{}, fmt.Errorf("scan wallet: %w", err)
	}
	bal, err := money.FromMinor(balanceMinor, currency)
	if err != nil {
		return wallet.Wallet{}, err
	}
	return wallet.Rehydrate(id, playerID, bal, version, createdAt, updatedAt)
}
