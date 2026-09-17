package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/app"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/event"
)

type InboxRepo struct {
	q querier
}

func NewInboxRepo(q querier) *InboxRepo {
	return &InboxRepo{q: q}
}

func (r *InboxRepo) Insert(ctx context.Context, consumerName, messageID, payloadHash string) (bool, string, error) {
	id, err := uuid.NewV7()
	if err != nil {
		id = uuid.New()
	}
	tag, err := r.q.Exec(ctx, `
		INSERT INTO inbox_messages (id, consumer_name, message_id, payload_hash, received_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (consumer_name, message_id) DO NOTHING`,
		id, consumerName, messageID, payloadHash, time.Now().UTC(),
	)
	if err != nil {
		return false, "", fmt.Errorf("insert inbox: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return true, "", nil
	}
	var existingHash string
	err = r.q.QueryRow(ctx, `
		SELECT payload_hash FROM inbox_messages
		WHERE consumer_name = $1 AND message_id = $2`,
		consumerName, messageID,
	).Scan(&existingHash)
	if err != nil {
		return false, "", fmt.Errorf("select inbox payload hash: %w", err)
	}
	return false, existingHash, nil
}

func (r *InboxRepo) MarkCompleted(ctx context.Context, consumerName, messageID string) error {
	_, err := r.q.Exec(ctx, `
		UPDATE inbox_messages SET completed_at = $3
		WHERE consumer_name = $1 AND message_id = $2`,
		consumerName, messageID, time.Now().UTC(),
	)
	if err != nil {
		return fmt.Errorf("complete inbox: %w", err)
	}
	return nil
}

type OutboxRepo struct {
	q querier
}

func NewOutboxRepo(q querier) *OutboxRepo {
	return &OutboxRepo{q: q}
}

func (r *OutboxRepo) Insert(ctx context.Context, env event.Envelope) error {
	payload, err := json.Marshal(map[string]any{
		"eventId":       env.EventID.String(),
		"eventType":     string(env.EventType),
		"aggregateId":   env.AggregateID.String(),
		"correlationId": env.CorrelationID,
		"causationId":   env.CausationID,
		"occurredAt":    env.OccurredAt.UTC().Format(time.RFC3339Nano),
		"version":       env.Version,
		"data":          env.Data,
	})
	if err != nil {
		return fmt.Errorf("marshal outbox payload: %w", err)
	}
	now := time.Now().UTC()
	_, err = r.q.Exec(ctx, `
		INSERT INTO outbox_events (
			id, aggregate_id, event_type, payload, correlation_id, causation_id,
			occurred_at, attempts, next_attempt_at, created_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,0,$8,$9)`,
		env.EventID, env.AggregateID, string(env.EventType), payload,
		nullIfEmpty(env.CorrelationID), env.CausationID, env.OccurredAt.UTC(), now, now,
	)
	if err != nil {
		return fmt.Errorf("insert outbox: %w", err)
	}
	return nil
}

func (r *OutboxRepo) ClaimBatch(ctx context.Context, now time.Time, lockUntil time.Time, limit int) ([]app.OutboxRecord, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := r.q.Query(ctx, `
		WITH cte AS (
			SELECT id
			FROM outbox_events
			WHERE published_at IS NULL
			  AND next_attempt_at <= $1
			  AND (locked_until IS NULL OR locked_until < $1)
			ORDER BY next_attempt_at ASC, id ASC
			FOR UPDATE SKIP LOCKED
			LIMIT $2
		)
		UPDATE outbox_events o
		SET locked_until = $3,
		    attempts = o.attempts + 1
		FROM cte
		WHERE o.id = cte.id
		RETURNING o.id, o.aggregate_id, o.event_type, o.payload, o.correlation_id, o.attempts, o.occurred_at`,
		now, limit, lockUntil,
	)
	if err != nil {
		return nil, fmt.Errorf("claim outbox: %w", err)
	}
	defer rows.Close()

	var out []app.OutboxRecord
	for rows.Next() {
		var rec app.OutboxRecord
		var corr *string
		if err := rows.Scan(&rec.ID, &rec.AggregateID, &rec.EventType, &rec.Payload, &corr, &rec.Attempts, &rec.OccurredAt); err != nil {
			return nil, fmt.Errorf("scan outbox claim: %w", err)
		}
		if corr != nil {
			rec.CorrelationID = *corr
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

func (r *OutboxRepo) MarkPublished(ctx context.Context, id uuid.UUID, publishedAt time.Time) error {
	_, err := r.q.Exec(ctx, `
		UPDATE outbox_events
		SET published_at = $2, locked_until = NULL
		WHERE id = $1`, id, publishedAt)
	if err != nil {
		return fmt.Errorf("mark outbox published: %w", err)
	}
	return nil
}

func (r *OutboxRepo) MarkPublishFailed(ctx context.Context, id uuid.UUID, attempts int, nextAttemptAt time.Time, clearLock bool) error {
	if clearLock {
		_, err := r.q.Exec(ctx, `
			UPDATE outbox_events
			SET attempts = $2, next_attempt_at = $3, locked_until = NULL
			WHERE id = $1`, id, attempts, nextAttemptAt)
		return err
	}
	_, err := r.q.Exec(ctx, `
		UPDATE outbox_events
		SET attempts = $2, next_attempt_at = $3
		WHERE id = $1`, id, attempts, nextAttemptAt)
	return err
}

type ReferenceRetryRepo struct {
	q querier
}

func NewReferenceRetryRepo(q querier) *ReferenceRetryRepo {
	return &ReferenceRetryRepo{q: q}
}

func (r *ReferenceRetryRepo) Upsert(ctx context.Context, transactionID uuid.UUID, attempts int, nextAttemptAt, expiresAt, updatedAt time.Time) error {
	_, err := r.q.Exec(ctx, `
		INSERT INTO reference_retry_state (transaction_id, attempts, next_attempt_at, expires_at, updated_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (transaction_id) DO UPDATE SET
			attempts = EXCLUDED.attempts,
			next_attempt_at = EXCLUDED.next_attempt_at,
			expires_at = EXCLUDED.expires_at,
			updated_at = EXCLUDED.updated_at`,
		transactionID, attempts, nextAttemptAt, expiresAt, updatedAt,
	)
	if err != nil {
		return fmt.Errorf("upsert reference retry: %w", err)
	}
	return nil
}

func (r *ReferenceRetryRepo) Get(ctx context.Context, transactionID uuid.UUID) (app.ReferenceRetry, bool, error) {
	var rec app.ReferenceRetry
	err := r.q.QueryRow(ctx, `
		SELECT transaction_id, attempts, next_attempt_at, expires_at, updated_at
		FROM reference_retry_state WHERE transaction_id = $1`, transactionID,
	).Scan(&rec.TransactionID, &rec.Attempts, &rec.NextAttemptAt, &rec.ExpiresAt, &rec.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.ReferenceRetry{}, false, nil
	}
	if err != nil {
		return app.ReferenceRetry{}, false, fmt.Errorf("get reference retry: %w", err)
	}
	return rec, true, nil
}

func (r *ReferenceRetryRepo) Delete(ctx context.Context, transactionID uuid.UUID) error {
	_, err := r.q.Exec(ctx, `DELETE FROM reference_retry_state WHERE transaction_id = $1`, transactionID)
	if err != nil {
		return fmt.Errorf("delete reference retry: %w", err)
	}
	return nil
}

var (
	_ app.InboxRepository          = (*InboxRepo)(nil)
	_ app.OutboxRepository         = (*OutboxRepo)(nil)
	_ app.ReferenceRetryRepository = (*ReferenceRetryRepo)(nil)
)
