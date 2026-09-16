package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/money"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/wallet"
)

type LedgerRepo struct {
	q querier
}

func NewLedgerRepo(q querier) *LedgerRepo {
	return &LedgerRepo{q: q}
}

func (r *LedgerRepo) Insert(ctx context.Context, entry wallet.LedgerEntry) error {
	_, err := r.q.Exec(ctx, `
		INSERT INTO wallet_ledger_entries (
			id, wallet_id, transaction_id, direction, amount_minor, currency,
			balance_before, balance_after, created_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		entry.ID(), entry.WalletID(), entry.TransactionID(), string(entry.Direction()),
		entry.Amount().AmountMinor(), entry.Amount().Currency(),
		entry.BalanceBefore().AmountMinor(), entry.BalanceAfter().AmountMinor(), entry.CreatedAt(),
	)
	if err != nil {
		return fmt.Errorf("insert ledger: %w", err)
	}
	return nil
}

func (r *LedgerRepo) ListByWallet(
	ctx context.Context,
	walletID uuid.UUID,
	afterCreatedAt *time.Time,
	afterID *uuid.UUID,
	limit int,
) ([]wallet.LedgerEntry, error) {
	if limit <= 0 {
		limit = 50
	}

	var (
		rows pgx.Rows
		err  error
	)
	if afterCreatedAt != nil && afterID != nil {
		rows, err = r.q.Query(ctx, `
			SELECT id, wallet_id, transaction_id, direction, amount_minor, currency,
			       balance_before, balance_after, created_at
			FROM wallet_ledger_entries
			WHERE wallet_id = $1
			  AND (created_at, id) > ($2, $3)
			ORDER BY created_at ASC, id ASC
			LIMIT $4`, walletID, *afterCreatedAt, *afterID, limit)
	} else {
		rows, err = r.q.Query(ctx, `
			SELECT id, wallet_id, transaction_id, direction, amount_minor, currency,
			       balance_before, balance_after, created_at
			FROM wallet_ledger_entries
			WHERE wallet_id = $1
			ORDER BY created_at ASC, id ASC
			LIMIT $2`, walletID, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("list ledger: %w", err)
	}
	defer rows.Close()

	var out []wallet.LedgerEntry
	for rows.Next() {
		var (
			id, wID, txID              uuid.UUID
			direction, currency        string
			amountMinor, before, after int64
			createdAt                  time.Time
		)
		if err := rows.Scan(&id, &wID, &txID, &direction, &amountMinor, &currency, &before, &after, &createdAt); err != nil {
			return nil, fmt.Errorf("scan ledger: %w", err)
		}
		amt, err := money.FromMinor(amountMinor, currency)
		if err != nil {
			return nil, err
		}
		bb, err := money.FromMinor(before, currency)
		if err != nil {
			return nil, err
		}
		ba, err := money.FromMinor(after, currency)
		if err != nil {
			return nil, err
		}
		entry, err := wallet.NewLedgerEntry(id, wID, txID, wallet.Direction(direction), amt, bb, ba, createdAt)
		if err != nil {
			return nil, err
		}
		out = append(out, entry)
	}
	return out, rows.Err()
}
