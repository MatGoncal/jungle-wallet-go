package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/apperr"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/money"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/wagering"
)

type TransactionRepo struct {
	q querier
}

func NewTransactionRepo(q querier) *TransactionRepo {
	return &TransactionRepo{q: q}
}

func (r *TransactionRepo) Insert(ctx context.Context, tx wagering.WagerTransaction) error {
	_, err := r.q.Exec(ctx, `
		INSERT INTO wager_transactions (
			id, origin, provider_id, external_transaction_id, idempotency_key, payload_hash,
			wallet_id, player_id, round_id, game_id, kind, amount_minor, currency,
			reference_external_transaction_id, reference_transaction_id, status, failure_code,
			result_balance_minor, created_at, updated_at
		) VALUES (
			$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20
		)`,
		tx.ID(), string(tx.Origin()), nullIfEmpty(tx.ProviderID()), nullIfEmpty(tx.ExternalTransactionID()),
		nullIfEmpty(tx.IdempotencyKey()), nullIfEmpty(tx.PayloadHash()),
		tx.WalletID(), tx.PlayerID(), nullIfEmpty(tx.RoundID()), nullIfEmpty(tx.GameID()),
		string(tx.Kind()), tx.Amount().AmountMinor(), tx.Amount().Currency(),
		nullIfEmpty(tx.ReferenceExternalTransactionID()), tx.ReferenceTransactionID(),
		string(tx.Status()), nullIfEmpty(string(tx.FailureCode())),
		tx.ResultBalanceMinor(), tx.CreatedAt(), tx.UpdatedAt(),
	)
	if err != nil {
		return fmt.Errorf("insert wager transaction: %w", err)
	}
	return nil
}

func (r *TransactionRepo) Update(ctx context.Context, tx wagering.WagerTransaction) error {
	_, err := r.q.Exec(ctx, `
		UPDATE wager_transactions SET
			reference_transaction_id = $2,
			status = $3,
			failure_code = $4,
			result_balance_minor = $5,
			updated_at = $6
		WHERE id = $1`,
		tx.ID(), tx.ReferenceTransactionID(), string(tx.Status()),
		nullIfEmpty(string(tx.FailureCode())), tx.ResultBalanceMinor(), tx.UpdatedAt(),
	)
	if err != nil {
		return fmt.Errorf("update wager transaction: %w", err)
	}
	return nil
}

func (r *TransactionRepo) GetByID(ctx context.Context, id uuid.UUID) (wagering.WagerTransaction, error) {
	tx, ok, err := r.getOne(ctx, `SELECT `+txColumns+` FROM wager_transactions WHERE id = $1`, id)
	if err != nil {
		return wagering.WagerTransaction{}, err
	}
	if !ok {
		return wagering.WagerTransaction{}, apperr.WrapFailure(apperr.CodeInvalidInput, "transaction not found", apperr.ErrNotFound)
	}
	return tx, nil
}

func (r *TransactionRepo) GetByIdempotencyKey(ctx context.Context, providerID, key string) (wagering.WagerTransaction, bool, error) {
	return r.getOne(ctx, `
		SELECT `+txColumns+` FROM wager_transactions
		WHERE provider_id = $1 AND idempotency_key = $2`, providerID, key)
}

func (r *TransactionRepo) GetByExternalID(ctx context.Context, providerID, externalID string) (wagering.WagerTransaction, bool, error) {
	return r.getOne(ctx, `
		SELECT `+txColumns+` FROM wager_transactions
		WHERE provider_id = $1 AND external_transaction_id = $2`, providerID, externalID)
}

// TryInsertIdempotency inserts or returns the existing row for the same (provider, key).
func (r *TransactionRepo) TryInsertIdempotency(ctx context.Context, tx wagering.WagerTransaction) (bool, wagering.WagerTransaction, error) {
	tag, err := r.q.Exec(ctx, `
		INSERT INTO wager_transactions (
			id, origin, provider_id, external_transaction_id, idempotency_key, payload_hash,
			wallet_id, player_id, round_id, game_id, kind, amount_minor, currency,
			reference_external_transaction_id, reference_transaction_id, status, failure_code,
			result_balance_minor, created_at, updated_at
		) VALUES (
			$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20
		)
		ON CONFLICT (provider_id, idempotency_key) WHERE origin = 'EXTERNAL' DO NOTHING`,
		tx.ID(), string(tx.Origin()), nullIfEmpty(tx.ProviderID()), nullIfEmpty(tx.ExternalTransactionID()),
		nullIfEmpty(tx.IdempotencyKey()), nullIfEmpty(tx.PayloadHash()),
		tx.WalletID(), tx.PlayerID(), nullIfEmpty(tx.RoundID()), nullIfEmpty(tx.GameID()),
		string(tx.Kind()), tx.Amount().AmountMinor(), tx.Amount().Currency(),
		nullIfEmpty(tx.ReferenceExternalTransactionID()), tx.ReferenceTransactionID(),
		string(tx.Status()), nullIfEmpty(string(tx.FailureCode())),
		tx.ResultBalanceMinor(), tx.CreatedAt(), tx.UpdatedAt(),
	)
	if err != nil {
		if isUniqueViolation(err) {
			return false, wagering.WagerTransaction{}, apperr.WrapFailure(apperr.CodeConflict, "unique identity conflict", apperr.ErrConflict)
		}
		return false, wagering.WagerTransaction{}, fmt.Errorf("try insert idempotency: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return true, tx, nil
	}
	existing, ok, err := r.GetByIdempotencyKey(ctx, tx.ProviderID(), tx.IdempotencyKey())
	if err != nil {
		return false, wagering.WagerTransaction{}, err
	}
	if !ok {
		return false, wagering.WagerTransaction{}, fmt.Errorf("idempotency conflict without existing row")
	}
	return false, existing, nil
}

const txColumns = `
	id, origin, provider_id, external_transaction_id, idempotency_key, payload_hash,
	wallet_id, player_id, round_id, game_id, kind, amount_minor, currency,
	reference_external_transaction_id, reference_transaction_id, status, failure_code,
	result_balance_minor, created_at, updated_at`

func txColumnsPrefixed(alias string) string {
	cols := []string{
		"id", "origin", "provider_id", "external_transaction_id", "idempotency_key", "payload_hash",
		"wallet_id", "player_id", "round_id", "game_id", "kind", "amount_minor", "currency",
		"reference_external_transaction_id", "reference_transaction_id", "status", "failure_code",
		"result_balance_minor", "created_at", "updated_at",
	}
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = alias + "." + c
	}
	return strings.Join(out, ", ")
}

func (r *TransactionRepo) getOne(ctx context.Context, sql string, args ...any) (wagering.WagerTransaction, bool, error) {
	var (
		id, walletID, playerID                                                   uuid.UUID
		origin, kind, status                                                     string
		providerID, externalID, idemKey, hash, roundID, gameID, refExt, failCode *string
		amountMinor                                                              int64
		currency                                                                 string
		refTxID                                                                  *uuid.UUID
		resultBalance                                                            *int64
		createdAt, updatedAt                                                     time.Time
	)
	err := r.q.QueryRow(ctx, sql, args...).Scan(
		&id, &origin, &providerID, &externalID, &idemKey, &hash,
		&walletID, &playerID, &roundID, &gameID, &kind, &amountMinor, &currency,
		&refExt, &refTxID, &status, &failCode, &resultBalance, &createdAt, &updatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return wagering.WagerTransaction{}, false, nil
	}
	if err != nil {
		return wagering.WagerTransaction{}, false, fmt.Errorf("scan wager transaction: %w", err)
	}
	amt, err := money.FromMinor(amountMinor, currency)
	if err != nil {
		return wagering.WagerTransaction{}, false, err
	}
	tx, err := wagering.Rehydrate(wagering.PersistState{
		ID:                             id,
		Origin:                         wagering.Origin(origin),
		ProviderID:                     deref(providerID),
		ExternalTransactionID:          deref(externalID),
		IdempotencyKey:                 deref(idemKey),
		PayloadHash:                    deref(hash),
		WalletID:                       walletID,
		PlayerID:                       playerID,
		RoundID:                        deref(roundID),
		GameID:                         deref(gameID),
		Kind:                           wagering.Kind(kind),
		Amount:                         amt,
		ReferenceExternalTransactionID: deref(refExt),
		ReferenceTransactionID:         refTxID,
		Status:                         wagering.Status(status),
		FailureCode:                    apperr.Code(deref(failCode)),
		ResultBalanceMinor:             resultBalance,
		CreatedAt:                      createdAt,
		UpdatedAt:                      updatedAt,
	})
	if err != nil {
		return wagering.WagerTransaction{}, false, err
	}
	return tx, true, nil
}

func (r *TransactionRepo) GetProcessedReversalByReference(ctx context.Context, referenceID uuid.UUID) (wagering.WagerTransaction, bool, error) {
	return r.getOne(ctx, `
		SELECT `+txColumns+` FROM wager_transactions
		WHERE reference_transaction_id = $1
		  AND status = 'PROCESSED'
		  AND kind IN ('REFUND', 'ROLLBACK')`, referenceID)
}

func (r *TransactionRepo) ListPendingReferencesDue(ctx context.Context, now time.Time, limit int) ([]wagering.WagerTransaction, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := r.q.Query(ctx, `
		SELECT `+txColumnsPrefixed("t")+`
		FROM wager_transactions t
		INNER JOIN reference_retry_state r ON r.transaction_id = t.id
		WHERE t.status = 'PENDING_REFERENCE'
		  AND r.next_attempt_at <= $1
		ORDER BY r.next_attempt_at ASC
		LIMIT $2`, now, limit)
	if err != nil {
		return nil, fmt.Errorf("list pending references: %w", err)
	}
	defer rows.Close()

	var out []wagering.WagerTransaction
	for rows.Next() {
		tx, err := scanTx(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, tx)
	}
	return out, rows.Err()
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanTx(row rowScanner) (wagering.WagerTransaction, error) {
	var (
		id, walletID, playerID                                                   uuid.UUID
		origin, kind, status                                                     string
		providerID, externalID, idemKey, hash, roundID, gameID, refExt, failCode *string
		amountMinor                                                              int64
		currency                                                                 string
		refTxID                                                                  *uuid.UUID
		resultBalance                                                            *int64
		createdAt, updatedAt                                                     time.Time
	)
	err := row.Scan(
		&id, &origin, &providerID, &externalID, &idemKey, &hash,
		&walletID, &playerID, &roundID, &gameID, &kind, &amountMinor, &currency,
		&refExt, &refTxID, &status, &failCode, &resultBalance, &createdAt, &updatedAt,
	)
	if err != nil {
		return wagering.WagerTransaction{}, fmt.Errorf("scan wager transaction: %w", err)
	}
	amt, err := money.FromMinor(amountMinor, currency)
	if err != nil {
		return wagering.WagerTransaction{}, err
	}
	return wagering.Rehydrate(wagering.PersistState{
		ID:                             id,
		Origin:                         wagering.Origin(origin),
		ProviderID:                     deref(providerID),
		ExternalTransactionID:          deref(externalID),
		IdempotencyKey:                 deref(idemKey),
		PayloadHash:                    deref(hash),
		WalletID:                       walletID,
		PlayerID:                       playerID,
		RoundID:                        deref(roundID),
		GameID:                         deref(gameID),
		Kind:                           wagering.Kind(kind),
		Amount:                         amt,
		ReferenceExternalTransactionID: deref(refExt),
		ReferenceTransactionID:         refTxID,
		Status:                         wagering.Status(status),
		FailureCode:                    apperr.Code(deref(failCode)),
		ResultBalanceMinor:             resultBalance,
		CreatedAt:                      createdAt,
		UpdatedAt:                      updatedAt,
	})
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
