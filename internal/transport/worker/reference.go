package worker

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/app"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/infra/observability"
)

const referenceLockTTL = 30 * time.Second

// ReferenceWorker polls PENDING_REFERENCE rows due for retry (backoff + TTL).
type ReferenceWorker struct {
	uow     app.UnitOfWork
	resolve *app.ResolvePendingReference
	log     *slog.Logger
	metrics *observability.Metrics
	cancel  context.CancelFunc
	done    chan struct{}
}

func NewReferenceWorker(
	uow app.UnitOfWork,
	resolve *app.ResolvePendingReference,
	log *slog.Logger,
	metrics *observability.Metrics,
) *ReferenceWorker {
	return &ReferenceWorker{uow: uow, resolve: resolve, log: log, metrics: metrics, done: make(chan struct{})}
}

func (w *ReferenceWorker) Start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	w.cancel = cancel
	go func() {
		defer close(w.done)
		w.loop(ctx)
	}()
}

func (w *ReferenceWorker) Stop(ctx context.Context) error {
	if w.cancel != nil {
		w.cancel()
	}
	select {
	case <-w.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *ReferenceWorker) loop(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := w.tick(ctx); err != nil {
				w.log.Error("reference worker tick failed", "error", err)
			}
		}
	}
}

func (w *ReferenceWorker) tick(ctx context.Context) error {
	now := time.Now().UTC()
	lockUntil := now.Add(referenceLockTTL)
	var ids []uuid.UUID
	err := w.uow.WithinTransaction(ctx, func(ctx context.Context, repos app.Repositories) error {
		txs, err := repos.Transactions().ClaimPendingReferencesDue(ctx, now, lockUntil, 20)
		if err != nil {
			return err
		}
		for _, tx := range txs {
			ids = append(ids, tx.ID())
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, id := range ids {
		if w.metrics != nil {
			w.metrics.Retries.Inc()
		}
		if err := w.resolve.Execute(ctx, app.ResolvePendingInput{
			TransactionID: id,
			CorrelationID: "reference-worker",
		}); err != nil {
			w.log.Error("resolve pending reference failed", "transactionId", id, "error", err)
		}
	}
	return nil
}
