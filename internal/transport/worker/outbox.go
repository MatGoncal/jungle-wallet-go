package worker

import (
	"context"
	"log/slog"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/app"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/infra/config"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/infra/observability"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/infra/sqs"
)

const outboxLockTTL = 30 * time.Second

// OutboxPublisher claims unpublished events with FOR UPDATE SKIP LOCKED and publishes to SQS FIFO.
// MessageDeduplicationId = eventId (preserved across republish). MessageGroupId = aggregateId.
type OutboxPublisher struct {
	client  *sqs.Client
	cfg     config.Config
	uow     app.UnitOfWork
	log     *slog.Logger
	metrics *observability.Metrics
	cancel  context.CancelFunc
	done    chan struct{}
}

func NewOutboxPublisher(
	client *sqs.Client,
	cfg config.Config,
	uow app.UnitOfWork,
	log *slog.Logger,
	metrics *observability.Metrics,
) *OutboxPublisher {
	return &OutboxPublisher{client: client, cfg: cfg, uow: uow, log: log, metrics: metrics, done: make(chan struct{})}
}

func (p *OutboxPublisher) Start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	p.cancel = cancel
	go func() {
		defer close(p.done)
		p.loop(ctx)
	}()
}

func (p *OutboxPublisher) Stop(ctx context.Context) error {
	if p.cancel != nil {
		p.cancel()
	}
	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *OutboxPublisher) loop(ctx context.Context) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := p.tick(ctx); err != nil {
				p.log.Error("outbox tick failed", "error", err)
			}
		}
	}
}

func (p *OutboxPublisher) tick(ctx context.Context) error {
	now := time.Now().UTC()
	lockUntil := now.Add(outboxLockTTL)
	var claimed []app.OutboxRecord
	err := p.uow.WithinTransaction(ctx, func(ctx context.Context, repos app.Repositories) error {
		var err error
		claimed, err = repos.Outbox().ClaimBatch(ctx, now, lockUntil, 20)
		return err
	})
	if err != nil {
		return err
	}
	if len(claimed) > 0 && p.metrics != nil {
		oldest := claimed[0].OccurredAt
		for _, rec := range claimed[1:] {
			if rec.OccurredAt.Before(oldest) {
				oldest = rec.OccurredAt
			}
		}
		p.metrics.OutboxLagSeconds.Set(now.Sub(oldest).Seconds())
	}
	for _, rec := range claimed {
		if err := p.publishOne(ctx, rec); err != nil {
			p.log.Error("outbox publish failed", "eventId", rec.ID, "error", err, "correlationId", rec.CorrelationID)
			if p.metrics != nil {
				p.metrics.Retries.Inc()
			}
			_ = p.uow.WithinTransaction(ctx, func(ctx context.Context, repos app.Repositories) error {
				shift := rec.Attempts
				if shift > 8 {
					shift = 8
				}
				next := time.Now().UTC().Add(time.Second << shift)
				return repos.Outbox().MarkPublishFailed(ctx, rec.ID, rec.Attempts, next, true)
			})
			continue
		}
		_ = p.uow.WithinTransaction(ctx, func(ctx context.Context, repos app.Repositories) error {
			return repos.Outbox().MarkPublished(ctx, rec.ID, time.Now().UTC())
		})
	}
	return nil
}

func (p *OutboxPublisher) publishOne(ctx context.Context, rec app.OutboxRecord) error {
	_, err := p.client.SQS().SendMessage(ctx, &awssqs.SendMessageInput{
		QueueUrl:               aws.String(p.cfg.EventsQueueURL),
		MessageBody:            aws.String(string(rec.Payload)),
		MessageGroupId:         aws.String(rec.AggregateID.String()),
		MessageDeduplicationId: aws.String(rec.ID.String()),
		MessageAttributes: map[string]types.MessageAttributeValue{
			"eventType": {DataType: aws.String("String"), StringValue: aws.String(rec.EventType)},
		},
	})
	return err
}
