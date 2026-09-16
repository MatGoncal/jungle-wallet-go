package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/google/uuid"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/app"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/apperr"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/money"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/wagering"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/infra/config"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/infra/observability"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/infra/sqs"
)

const consumerName = "wager-transactions-consumer"

// MessageGroupId = walletId; MessageDeduplicationId = idempotency key (documented).
// VisibilityTimeout=30s, maxReceiveCount=5 before DLQ (provisioned in LocalStack init).

type wagerEnvelope struct {
	MessageID  string           `json:"messageId"`
	Type       string           `json:"type"`
	OccurredAt string           `json:"occurredAt"`
	Data       wagerMessageData `json:"data"`
}

type wagerMessageData struct {
	ProviderID            string `json:"providerId"`
	ExternalTransactionID string `json:"externalTransactionId"`
	IdempotencyKey        string `json:"idempotencyKey"`
	PlayerID              string `json:"playerId"`
	WalletID              string `json:"walletId"`
	RoundID               string `json:"roundId"`
	GameID                string `json:"gameId"`
	Kind                  string `json:"kind"`
	Money                 struct {
		Amount   string `json:"amount"`
		Currency string `json:"currency"`
	} `json:"money"`
	ReferenceExternalTransactionID string `json:"referenceExternalTransactionId"`
}

// AfterCommitHook runs after the SQL commit and before DeleteMessage.
// Tests use it to simulate a crash between commit and ack.
type AfterCommitHook func(ctx context.Context) error

type Consumer struct {
	client          *sqs.Client
	cfg             config.Config
	uow             app.UnitOfWork
	process         *app.ProcessWagerTransaction
	log             *slog.Logger
	metrics         *observability.Metrics
	AfterCommitHook AfterCommitHook

	cancel   context.CancelFunc
	done     chan struct{}
	wg       sync.WaitGroup
	mu       sync.Mutex
	inFlight map[string]types.Message // receipt handle -> message, for visibility release on timeout
}

func NewConsumer(
	client *sqs.Client,
	cfg config.Config,
	uow app.UnitOfWork,
	process *app.ProcessWagerTransaction,
	log *slog.Logger,
	metrics *observability.Metrics,
) *Consumer {
	return &Consumer{
		client: client, cfg: cfg, uow: uow, process: process, log: log, metrics: metrics,
		done: make(chan struct{}), inFlight: map[string]types.Message{},
	}
}

func (c *Consumer) Start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	c.cancel = cancel
	go func() {
		defer close(c.done)
		c.loop(ctx)
	}()
}

// Stop cancels polling, waits for in-flight work up to ctx deadline, then releases remaining visibility.
func (c *Consumer) Stop(ctx context.Context) error {
	if c.cancel != nil {
		c.cancel()
	}
	finished := make(chan struct{})
	go func() {
		c.wg.Wait()
		close(finished)
	}()
	select {
	case <-finished:
	case <-ctx.Done():
		// Parent stop ctx is already done; WithoutCancel keeps values and allows SQS release.
		releaseCtx, releaseCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		c.releaseInFlight(releaseCtx)
		releaseCancel()
	}
	select {
	case <-c.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Consumer) releaseInFlight(ctx context.Context) {
	c.mu.Lock()
	msgs := make([]types.Message, 0, len(c.inFlight))
	for _, m := range c.inFlight {
		msgs = append(msgs, m)
	}
	c.mu.Unlock()
	for _, msg := range msgs {
		_, _ = c.client.SQS().ChangeMessageVisibility(ctx, &awssqs.ChangeMessageVisibilityInput{
			QueueUrl:          aws.String(c.cfg.WagerQueueURL),
			ReceiptHandle:     msg.ReceiptHandle,
			VisibilityTimeout: 0,
		})
		if c.metrics != nil {
			c.metrics.Retries.Inc()
		}
	}
}

func (c *Consumer) track(msg types.Message) {
	c.mu.Lock()
	c.inFlight[aws.ToString(msg.ReceiptHandle)] = msg
	c.mu.Unlock()
}

func (c *Consumer) untrack(msg types.Message) {
	c.mu.Lock()
	delete(c.inFlight, aws.ToString(msg.ReceiptHandle))
	c.mu.Unlock()
}

func (c *Consumer) loop(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		out, err := c.client.SQS().ReceiveMessage(ctx, &awssqs.ReceiveMessageInput{
			QueueUrl:              aws.String(c.cfg.WagerQueueURL),
			MaxNumberOfMessages:   5,
			WaitTimeSeconds:       10,
			VisibilityTimeout:     30,
			MessageAttributeNames: []string{"All"},
			AttributeNames:        []types.QueueAttributeName{types.QueueAttributeNameAll},
		})
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			c.log.Error("sqs receive failed", "error", err)
			time.Sleep(time.Second)
			continue
		}
		for _, msg := range out.Messages {
			if ctx.Err() != nil {
				// Stop accepting new work; release visibility so another instance can pick up.
				_, _ = c.client.SQS().ChangeMessageVisibility(ctx, &awssqs.ChangeMessageVisibilityInput{
					QueueUrl:          aws.String(c.cfg.WagerQueueURL),
					ReceiptHandle:     msg.ReceiptHandle,
					VisibilityTimeout: 0,
				})
				continue
			}
			c.wg.Add(1)
			c.track(msg)
			func(msg types.Message) {
				defer c.wg.Done()
				defer c.untrack(msg)
				if err := c.handle(ctx, msg); err != nil {
					observability.LoggerFromContext(c.log, ctx).Error(
						"sqs message handling failed",
						"error", err,
						"messageId", aws.ToString(msg.MessageId),
					)
				}
			}(msg)
		}
	}
}

func (c *Consumer) handle(ctx context.Context, msg types.Message) error {
	start := time.Now()
	body := aws.ToString(msg.Body)
	hash := sha256Hex(body)

	var env wagerEnvelope
	if err := json.Unmarshal([]byte(body), &env); err != nil || env.MessageID == "" || env.Data.IdempotencyKey == "" {
		c.log.Warn("malformed sqs message -> leave for DLQ via receive count", "error", err)
		if c.metrics != nil {
			c.metrics.DLQMessages.Inc()
		}
		_, _ = c.client.SQS().ChangeMessageVisibility(ctx, &awssqs.ChangeMessageVisibilityInput{
			QueueUrl:          aws.String(c.cfg.WagerQueueURL),
			ReceiptHandle:     msg.ReceiptHandle,
			VisibilityTimeout: 0,
		})
		return fmt.Errorf("malformed message")
	}

	ctx = observability.WithCorrelationID(ctx, env.MessageID)
	ctx = observability.WithMessageID(ctx, env.MessageID)
	ctx = observability.WithProviderID(ctx, env.Data.ProviderID)
	ctx = observability.WithWalletID(ctx, env.Data.WalletID)
	log := observability.LoggerFromContext(c.log, ctx)

	playerID, err := uuid.Parse(env.Data.PlayerID)
	if err != nil {
		return c.poison(ctx, msg, err)
	}
	walletID, err := uuid.Parse(env.Data.WalletID)
	if err != nil {
		return c.poison(ctx, msg, err)
	}
	amt, err := money.Parse(env.Data.Money.Amount, env.Data.Money.Currency)
	if err != nil {
		return c.poison(ctx, msg, err)
	}

	err = c.uow.WithinTransaction(ctx, func(ctx context.Context, repos app.Repositories) error {
		inserted, err := repos.Inbox().Insert(ctx, consumerName, env.MessageID, hash)
		if err != nil {
			return err
		}
		if !inserted {
			if c.metrics != nil {
				c.metrics.IdempotentReplays.Inc()
			}
			return repos.Inbox().MarkCompleted(ctx, consumerName, env.MessageID)
		}
		out, err := c.process.ExecuteWithRepos(ctx, repos, app.ProcessWagerInput{
			ProviderID:                     env.Data.ProviderID,
			ExternalTransactionID:          env.Data.ExternalTransactionID,
			IdempotencyKey:                 env.Data.IdempotencyKey,
			PlayerID:                       playerID,
			WalletID:                       walletID,
			RoundID:                        env.Data.RoundID,
			GameID:                         env.Data.GameID,
			Kind:                           wagering.Kind(env.Data.Kind),
			Money:                          amt,
			ReferenceExternalTransactionID: env.Data.ReferenceExternalTransactionID,
			CorrelationID:                  env.MessageID,
		})
		if err != nil {
			if f, ok := apperr.AsFailure(err); ok && f.Code == apperr.CodeConflict {
				if c.metrics != nil {
					c.metrics.ConcurrencyConflicts.Inc()
				}
				return repos.Inbox().MarkCompleted(ctx, consumerName, env.MessageID)
			}
			return err
		}
		if c.metrics != nil {
			c.metrics.TransactionsTotal.WithLabelValues(string(out.Transaction.Status()), env.Data.Kind, "sqs").Inc()
			if out.IdempotentReplay {
				c.metrics.IdempotentReplays.Inc()
			}
		}
		return repos.Inbox().MarkCompleted(ctx, consumerName, env.MessageID)
	})
	if err != nil {
		return err
	}

	if c.metrics != nil {
		c.metrics.ObserveProcessing("sqs", env.Data.Kind, time.Since(start))
	}

	if c.AfterCommitHook != nil {
		if err := c.AfterCommitHook(ctx); err != nil {
			log.Warn("after-commit hook aborted before DeleteMessage", "error", err)
			return err
		}
	}

	_, err = c.client.SQS().DeleteMessage(ctx, &awssqs.DeleteMessageInput{
		QueueUrl:      aws.String(c.cfg.WagerQueueURL),
		ReceiptHandle: msg.ReceiptHandle,
	})
	return err
}

func (c *Consumer) poison(ctx context.Context, msg types.Message, cause error) error {
	observability.LoggerFromContext(c.log, ctx).Warn("poison message", "error", cause)
	if c.metrics != nil {
		c.metrics.DLQMessages.Inc()
		c.metrics.Retries.Inc()
	}
	_, _ = c.client.SQS().ChangeMessageVisibility(ctx, &awssqs.ChangeMessageVisibilityInput{
		QueueUrl:          aws.String(c.cfg.WagerQueueURL),
		ReceiptHandle:     msg.ReceiptHandle,
		VisibilityTimeout: 0,
	})
	return cause
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
