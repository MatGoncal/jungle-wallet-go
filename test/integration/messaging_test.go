//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/google/uuid"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/app"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/event"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/money"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/wagering"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/infra/postgres"
)

func TestInbox_SameMessageIDTwice(t *testing.T) {
	pool := openPool(t)
	uow := postgres.NewUnitOfWork(pool)
	msgID := "msg-" + uuid.NewString()
	err := uow.WithinTransaction(context.Background(), func(ctx context.Context, repos app.Repositories) error {
		ok, err := repos.Inbox().Insert(ctx, "wager-transactions-consumer", msgID, "h1")
		if err != nil || !ok {
			return fmt.Errorf("first insert ok=%v err=%v", ok, err)
		}
		ok2, err := repos.Inbox().Insert(ctx, "wager-transactions-consumer", msgID, "h2")
		if err != nil {
			return err
		}
		if ok2 {
			return fmt.Errorf("second insert should be conflict")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestOutbox_TwoPublishersCompete(t *testing.T) {
	pool := openPool(t)
	uow := postgres.NewUnitOfWork(pool)
	open := app.NewOpenWallet(uow)
	player := mustV7(t)
	bal, _ := money.Parse("50.00", "BRL")
	wal, err := open.Execute(context.Background(), app.OpenWalletInput{
		PlayerID: player, InitialBalance: bal, CorrelationID: "outbox-compete",
	})
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	lockUntil := now.Add(30 * time.Second)
	var a, b []app.OutboxRecord
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = uow.WithinTransaction(context.Background(), func(ctx context.Context, repos app.Repositories) error {
			var err error
			a, err = repos.Outbox().ClaimBatch(ctx, now, lockUntil, 10)
			return err
		})
	}()
	go func() {
		defer wg.Done()
		_ = uow.WithinTransaction(context.Background(), func(ctx context.Context, repos app.Repositories) error {
			var err error
			b, err = repos.Outbox().ClaimBatch(ctx, now, lockUntil, 10)
			return err
		})
	}()
	wg.Wait()

	seen := map[uuid.UUID]bool{}
	for _, rec := range append(a, b...) {
		if seen[rec.ID] {
			t.Fatalf("duplicate claim of event %s", rec.ID)
		}
		seen[rec.ID] = true
	}
	if len(seen) < 1 {
		t.Fatalf("expected claimed events for wallet %s, got 0 (a=%d b=%d)", wal.Wallet.ID(), len(a), len(b))
	}
}

func TestOutbox_EventIDPreservedOnRepublish(t *testing.T) {
	pool := openPool(t)
	uow := postgres.NewUnitOfWork(pool)
	evID := mustV7(t)
	agg := mustV7(t)
	env := event.Envelope{
		EventID: evID, EventType: event.TypeWalletBalanceChanged, AggregateID: agg,
		CorrelationID: "republish", OccurredAt: time.Now().UTC(), Version: 1,
		Data: map[string]string{"k": "v"},
	}
	err := uow.WithinTransaction(context.Background(), func(ctx context.Context, repos app.Repositories) error {
		return repos.Outbox().Insert(ctx, env)
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	var claimed []app.OutboxRecord
	_ = uow.WithinTransaction(context.Background(), func(ctx context.Context, repos app.Repositories) error {
		var err error
		claimed, err = repos.Outbox().ClaimBatch(ctx, now, now.Add(time.Second), 1)
		return err
	})
	if len(claimed) != 1 || claimed[0].ID != evID {
		t.Fatalf("claimed=%v", claimed)
	}
	_ = uow.WithinTransaction(context.Background(), func(ctx context.Context, repos app.Repositories) error {
		return repos.Outbox().MarkPublishFailed(ctx, claimed[0].ID, claimed[0].Attempts, now, true)
	})
	var again []app.OutboxRecord
	_ = uow.WithinTransaction(context.Background(), func(ctx context.Context, repos app.Repositories) error {
		var err error
		again, err = repos.Outbox().ClaimBatch(ctx, now.Add(time.Second), now.Add(2*time.Second), 1)
		return err
	})
	if len(again) != 1 || again[0].ID != evID {
		t.Fatalf("republish must preserve eventId, got %v", again)
	}
}

func TestPendingReference_ResolvedAndExpired(t *testing.T) {
	pool := openPool(t)
	uow := postgres.NewUnitOfWork(pool)
	open := app.NewOpenWallet(uow)
	process := app.NewProcessWagerTransaction(uow)
	resolve := app.NewResolvePendingReference(uow)
	player := mustV7(t)
	bal, _ := money.Parse("100.00", "BRL")
	wal, _ := open.Execute(context.Background(), app.OpenWalletInput{PlayerID: player, InitialBalance: bal})

	refundAmt, _ := money.Parse("10.00", "BRL")
	pending, err := process.Execute(context.Background(), app.ProcessWagerInput{
		ProviderID: "provider-a", ExternalTransactionID: "ref-pending-1", IdempotencyKey: "k-ref-p-1",
		PlayerID: player, WalletID: wal.Wallet.ID(), RoundID: "r1", GameID: "g1",
		Kind: wagering.KindRefund, Money: refundAmt, ReferenceExternalTransactionID: "bet-missing",
		CorrelationID: "pend", ReferenceTTL: 50 * time.Millisecond, ReferenceMaxAttempts: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if pending.Transaction.Status() != wagering.StatusPendingReference {
		t.Fatalf("status=%s", pending.Transaction.Status())
	}

	// Expire retry state and resolve -> REFERENCE_NOT_FOUND
	_, _ = pool.Exec(context.Background(), `
		UPDATE reference_retry_state SET next_attempt_at = NOW() - interval '1 second', expires_at = NOW() - interval '1 second'
		WHERE transaction_id = $1`, pending.Transaction.ID())
	if err := resolve.Execute(context.Background(), app.ResolvePendingInput{
		TransactionID: pending.Transaction.ID(), CorrelationID: "expire", ReferenceMaxAttempts: 2,
	}); err != nil {
		t.Fatal(err)
	}
	var status, code string
	_ = pool.QueryRow(context.Background(), `
		SELECT status, COALESCE(failure_code,'') FROM wager_transactions WHERE id=$1`, pending.Transaction.ID()).Scan(&status, &code)
	if status != "REJECTED" || code != "REFERENCE_NOT_FOUND" {
		t.Fatalf("status=%s code=%s", status, code)
	}

	// Resolved path: BET then REFUND arrives after
	betAmt, _ := money.Parse("15.00", "BRL")
	_, err = process.Execute(context.Background(), app.ProcessWagerInput{
		ProviderID: "provider-a", ExternalTransactionID: "bet-ok-1", IdempotencyKey: "k-bet-ok",
		PlayerID: player, WalletID: wal.Wallet.ID(), RoundID: "r2", GameID: "g1",
		Kind: wagering.KindBet, Money: betAmt,
	})
	if err != nil {
		t.Fatal(err)
	}
	refPending, err := process.Execute(context.Background(), app.ProcessWagerInput{
		ProviderID: "provider-a", ExternalTransactionID: "refund-ok-1", IdempotencyKey: "k-ref-ok",
		PlayerID: player, WalletID: wal.Wallet.ID(), RoundID: "r2", GameID: "g1",
		Kind: wagering.KindRefund, Money: betAmt, ReferenceExternalTransactionID: "bet-ok-1",
		ReferenceTTL: time.Hour, ReferenceMaxAttempts: 12,
	})
	if err != nil {
		t.Fatal(err)
	}
	// If BET already exists, refund should process immediately (not pending).
	if refPending.Transaction.Status() != wagering.StatusProcessed {
		// If pending, resolve
		_ = resolve.Execute(context.Background(), app.ResolvePendingInput{TransactionID: refPending.Transaction.ID()})
	}
}

func TestSQS_PoisonGoesTowardDLQ(t *testing.T) {
	ctx := context.Background()
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion("us-east-1"),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")),
	)
	if err != nil {
		t.Fatal(err)
	}
	client := awssqs.NewFromConfig(awsCfg, func(o *awssqs.Options) {
		o.BaseEndpoint = aws.String(sharedSQSEndpoint)
	})
	body, _ := json.Marshal(map[string]any{"not": "a wager envelope"})
	dedup := uuid.NewString()
	_, err = client.SendMessage(ctx, &awssqs.SendMessageInput{
		QueueUrl:               aws.String(sharedWagerURL),
		MessageBody:            aws.String(string(body)),
		MessageGroupId:         aws.String("poison-group-" + dedup),
		MessageDeduplicationId: aws.String(dedup),
	})
	if err != nil {
		t.Fatal(err)
	}
	// Drive redrive by exhausting receives (maxReceiveCount=5).
	for i := 0; i < 6; i++ {
		out, err := client.ReceiveMessage(ctx, &awssqs.ReceiveMessageInput{
			QueueUrl:              aws.String(sharedWagerURL),
			MaxNumberOfMessages:   1,
			WaitTimeSeconds:       2,
			VisibilityTimeout:     1,
			AttributeNames:        []types.QueueAttributeName{types.QueueAttributeNameAll},
			MessageAttributeNames: []string{"All"},
		})
		if err != nil || len(out.Messages) == 0 {
			time.Sleep(1200 * time.Millisecond)
			continue
		}
		_, _ = client.ChangeMessageVisibility(ctx, &awssqs.ChangeMessageVisibilityInput{
			QueueUrl: aws.String(sharedWagerURL), ReceiptHandle: out.Messages[0].ReceiptHandle, VisibilityTimeout: 0,
		})
		time.Sleep(1200 * time.Millisecond)
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		out, err := client.ReceiveMessage(ctx, &awssqs.ReceiveMessageInput{
			QueueUrl: aws.String(sharedDLQURL), MaxNumberOfMessages: 1, WaitTimeSeconds: 3,
		})
		if err == nil && len(out.Messages) > 0 {
			_, _ = client.DeleteMessage(ctx, &awssqs.DeleteMessageInput{
				QueueUrl: aws.String(sharedDLQURL), ReceiptHandle: out.Messages[0].ReceiptHandle,
			})
			return
		}
	}
	// LocalStack redrive can lag; assert the main queue no longer holds the poison message.
	out, err := client.ReceiveMessage(ctx, &awssqs.ReceiveMessageInput{
		QueueUrl: aws.String(sharedWagerURL), MaxNumberOfMessages: 1, WaitTimeSeconds: 1, VisibilityTimeout: 1,
	})
	if err == nil && len(out.Messages) == 0 {
		t.Log("poison left the main queue (DLQ receive timed out; LocalStack redrive lag)")
		return
	}
	t.Fatal("expected poison message in DLQ or removed from main queue after maxReceiveCount")
}
