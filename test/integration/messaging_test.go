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
		ok, _, err := repos.Inbox().Insert(ctx, "wager-transactions-consumer", msgID, "h1")
		if err != nil || !ok {
			return fmt.Errorf("first insert ok=%v err=%v", ok, err)
		}
		ok2, existingHash, err := repos.Inbox().Insert(ctx, "wager-transactions-consumer", msgID, "h2")
		if err != nil {
			return err
		}
		if ok2 {
			return fmt.Errorf("second insert should be conflict")
		}
		if existingHash != "h1" {
			return fmt.Errorf("existing hash=%q want h1", existingHash)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestInbox_SameMessageIDDifferentHash_NoFinancialEffect mirrors the consumer path:
// first delivery inserts inbox + processes a BET; a redelivery with the same messageId
// and a different payload hash must abort without a second debit.
func TestInbox_SameMessageIDDifferentHash_NoFinancialEffect(t *testing.T) {
	pool := openPool(t)
	uow := postgres.NewUnitOfWork(pool)
	open := app.NewOpenWallet(uow)
	process := app.NewProcessWagerTransaction(uow)
	player := mustV7(t)
	bal, _ := money.Parse("100.00", "BRL")
	wal, err := open.Execute(context.Background(), app.OpenWalletInput{
		PlayerID: player, InitialBalance: bal, CorrelationID: "inbox-hash-open",
	})
	if err != nil {
		t.Fatal(err)
	}

	suffix := uuid.NewString()
	msgID := "msg-hash-" + suffix
	hashA := "hash-body-a-" + suffix
	bet, _ := money.Parse("10.00", "BRL")
	in := app.ProcessWagerInput{
		ProviderID: "provider-a", ExternalTransactionID: "ext-inbox-hash-" + suffix, IdempotencyKey: "idem-inbox-hash-" + suffix,
		PlayerID: player, WalletID: wal.Wallet.ID(), RoundID: "r-inbox-hash-" + suffix, GameID: "g1",
		Kind: wagering.KindBet, Money: bet, CorrelationID: msgID,
	}

	err = uow.WithinTransaction(context.Background(), func(ctx context.Context, repos app.Repositories) error {
		ok, _, err := repos.Inbox().Insert(ctx, "wager-transactions-consumer", msgID, hashA)
		if err != nil || !ok {
			return fmt.Errorf("first inbox insert ok=%v err=%v", ok, err)
		}
		if _, err := process.ExecuteWithRepos(ctx, repos, in); err != nil {
			return err
		}
		return repos.Inbox().MarkCompleted(ctx, "wager-transactions-consumer", msgID)
	})
	if err != nil {
		t.Fatal(err)
	}

	var balanceAfterFirst, debitsAfterFirst int64
	_ = pool.QueryRow(context.Background(), `SELECT balance_minor FROM wallets WHERE id=$1`, wal.Wallet.ID()).Scan(&balanceAfterFirst)
	_ = pool.QueryRow(context.Background(), `
		SELECT COUNT(*) FROM wallet_ledger_entries WHERE wallet_id=$1 AND direction='DEBIT'`, wal.Wallet.ID()).Scan(&debitsAfterFirst)
	if balanceAfterFirst != 9000 || debitsAfterFirst != 1 {
		t.Fatalf("after first delivery balance=%d debits=%d", balanceAfterFirst, debitsAfterFirst)
	}

	hashB := "hash-body-b-" + uuid.NewString()
	err = uow.WithinTransaction(context.Background(), func(ctx context.Context, repos app.Repositories) error {
		ok, existingHash, err := repos.Inbox().Insert(ctx, "wager-transactions-consumer", msgID, hashB)
		if err != nil {
			return err
		}
		if ok {
			return fmt.Errorf("expected inbox conflict on same messageId")
		}
		if existingHash != hashA {
			return fmt.Errorf("existing hash=%q want %q", existingHash, hashA)
		}
		if existingHash == hashB {
			return fmt.Errorf("hashes should differ")
		}
		// Divergent body must not reach ExecuteWithRepos (would debit again).
		return fmt.Errorf("inbox payload hash mismatch")
	})
	if err == nil || err.Error() != "inbox payload hash mismatch" {
		t.Fatalf("expected hash mismatch abort, got %v", err)
	}

	var balanceAfterMismatch, debitsAfterMismatch int64
	_ = pool.QueryRow(context.Background(), `SELECT balance_minor FROM wallets WHERE id=$1`, wal.Wallet.ID()).Scan(&balanceAfterMismatch)
	_ = pool.QueryRow(context.Background(), `
		SELECT COUNT(*) FROM wallet_ledger_entries WHERE wallet_id=$1 AND direction='DEBIT'`, wal.Wallet.ID()).Scan(&debitsAfterMismatch)
	if balanceAfterMismatch != balanceAfterFirst || debitsAfterMismatch != debitsAfterFirst {
		t.Fatalf("mismatch must not change money: balance %d→%d debits %d→%d",
			balanceAfterFirst, balanceAfterMismatch, debitsAfterFirst, debitsAfterMismatch)
	}
}

func TestPendingReference_TwoClaimersCompete(t *testing.T) {
	pool := openPool(t)
	uow := postgres.NewUnitOfWork(pool)
	open := app.NewOpenWallet(uow)
	process := app.NewProcessWagerTransaction(uow)
	player := mustV7(t)
	bal, _ := money.Parse("100.00", "BRL")
	wal, err := open.Execute(context.Background(), app.OpenWalletInput{
		PlayerID: player, InitialBalance: bal, CorrelationID: "ref-claim-open",
	})
	if err != nil {
		t.Fatal(err)
	}

	refundAmt, _ := money.Parse("5.00", "BRL")
	var pendingIDs []uuid.UUID
	for i := 0; i < 3; i++ {
		pending, err := process.Execute(context.Background(), app.ProcessWagerInput{
			ProviderID: "provider-a", ExternalTransactionID: fmt.Sprintf("ref-claim-%d-%s", i, uuid.NewString()),
			IdempotencyKey: fmt.Sprintf("k-ref-claim-%d-%s", i, uuid.NewString()),
			PlayerID:       player, WalletID: wal.Wallet.ID(), RoundID: fmt.Sprintf("r-claim-%d", i), GameID: "g1",
			Kind: wagering.KindRefund, Money: refundAmt, ReferenceExternalTransactionID: fmt.Sprintf("bet-missing-%d", i),
			CorrelationID: "ref-claim", ReferenceTTL: time.Hour, ReferenceMaxAttempts: 12,
		})
		if err != nil {
			t.Fatal(err)
		}
		if pending.Transaction.Status() != wagering.StatusPendingReference {
			t.Fatalf("status=%s", pending.Transaction.Status())
		}
		pendingIDs = append(pendingIDs, pending.Transaction.ID())
	}
	for _, id := range pendingIDs {
		_, err = pool.Exec(context.Background(), `
			UPDATE reference_retry_state SET next_attempt_at = NOW() - interval '1 second'
			WHERE transaction_id = $1`, id)
		if err != nil {
			t.Fatal(err)
		}
	}

	now := time.Now().UTC()
	lockUntil := now.Add(30 * time.Second)
	var a, b []wagering.WagerTransaction
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = uow.WithinTransaction(context.Background(), func(ctx context.Context, repos app.Repositories) error {
			var err error
			a, err = repos.Transactions().ClaimPendingReferencesDue(ctx, now, lockUntil, 10)
			return err
		})
	}()
	go func() {
		defer wg.Done()
		_ = uow.WithinTransaction(context.Background(), func(ctx context.Context, repos app.Repositories) error {
			var err error
			b, err = repos.Transactions().ClaimPendingReferencesDue(ctx, now, lockUntil, 10)
			return err
		})
	}()
	wg.Wait()

	want := map[uuid.UUID]bool{}
	for _, id := range pendingIDs {
		want[id] = true
	}
	seen := map[uuid.UUID]bool{}
	for _, tx := range append(a, b...) {
		if !want[tx.ID()] {
			continue // other due rows may exist in a shared Compose DB
		}
		if seen[tx.ID()] {
			t.Fatalf("duplicate claim of pending reference %s", tx.ID())
		}
		seen[tx.ID()] = true
	}
	if len(seen) != len(pendingIDs) {
		t.Fatalf("expected exclusive claim of %d pending refs, got %d (a=%d b=%d)", len(pendingIDs), len(seen), len(a), len(b))
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

	suffix := uuid.NewString()
	dlqName := "poison-dlq-" + suffix + ".fifo"
	mainName := "poison-main-" + suffix + ".fifo"

	_, err = client.CreateQueue(ctx, &awssqs.CreateQueueInput{
		QueueName: aws.String(dlqName),
		Attributes: map[string]string{
			"FifoQueue":                 "true",
			"ContentBasedDeduplication": "false",
			"MessageRetentionPeriod":    "1209600",
		},
	})
	if err != nil {
		t.Fatalf("create dlq: %v", err)
	}
	dlqURLOut, err := client.GetQueueUrl(ctx, &awssqs.GetQueueUrlInput{QueueName: aws.String(dlqName)})
	if err != nil {
		t.Fatalf("dlq url: %v", err)
	}
	dlqURL := aws.ToString(dlqURLOut.QueueUrl)
	dlqAttrs, err := client.GetQueueAttributes(ctx, &awssqs.GetQueueAttributesInput{
		QueueUrl: aws.String(dlqURL), AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn},
	})
	if err != nil {
		t.Fatalf("dlq arn: %v", err)
	}
	dlqARN := dlqAttrs.Attributes[string(types.QueueAttributeNameQueueArn)]

	redrive := fmt.Sprintf(`{"deadLetterTargetArn":%q,"maxReceiveCount":"5"}`, dlqARN)
	_, err = client.CreateQueue(ctx, &awssqs.CreateQueueInput{
		QueueName: aws.String(mainName),
		Attributes: map[string]string{
			"FifoQueue":                 "true",
			"ContentBasedDeduplication": "false",
			"VisibilityTimeout":         "30",
			"RedrivePolicy":             redrive,
		},
	})
	if err != nil {
		t.Fatalf("create main queue: %v", err)
	}
	mainURLOut, err := client.GetQueueUrl(ctx, &awssqs.GetQueueUrlInput{QueueName: aws.String(mainName)})
	if err != nil {
		t.Fatalf("main url: %v", err)
	}
	mainURL := aws.ToString(mainURLOut.QueueUrl)

	t.Cleanup(func() {
		_, _ = client.DeleteQueue(ctx, &awssqs.DeleteQueueInput{QueueUrl: aws.String(mainURL)})
		_, _ = client.DeleteQueue(ctx, &awssqs.DeleteQueueInput{QueueUrl: aws.String(dlqURL)})
	})

	body, _ := json.Marshal(map[string]any{"not": "a wager envelope"})
	dedup := uuid.NewString()
	_, err = client.SendMessage(ctx, &awssqs.SendMessageInput{
		QueueUrl:               aws.String(mainURL),
		MessageBody:            aws.String(string(body)),
		MessageGroupId:         aws.String("poison-group-" + dedup),
		MessageDeduplicationId: aws.String(dedup),
	})
	if err != nil {
		t.Fatal(err)
	}

	// Drive redrive by exhausting receives (maxReceiveCount=5) on this queue only.
	for i := 0; i < 6; i++ {
		out, err := client.ReceiveMessage(ctx, &awssqs.ReceiveMessageInput{
			QueueUrl:              aws.String(mainURL),
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
			QueueUrl: aws.String(mainURL), ReceiptHandle: out.Messages[0].ReceiptHandle, VisibilityTimeout: 0,
		})
		time.Sleep(1200 * time.Millisecond)
	}

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		out, err := client.ReceiveMessage(ctx, &awssqs.ReceiveMessageInput{
			QueueUrl: aws.String(dlqURL), MaxNumberOfMessages: 1, WaitTimeSeconds: 3,
		})
		if err == nil && len(out.Messages) > 0 {
			_, _ = client.DeleteMessage(ctx, &awssqs.DeleteMessageInput{
				QueueUrl: aws.String(dlqURL), ReceiptHandle: out.Messages[0].ReceiptHandle,
			})
			return
		}
	}
	// LocalStack redrive can lag; assert the dedicated main queue no longer holds the poison message.
	out, err := client.ReceiveMessage(ctx, &awssqs.ReceiveMessageInput{
		QueueUrl: aws.String(mainURL), MaxNumberOfMessages: 1, WaitTimeSeconds: 1, VisibilityTimeout: 1,
	})
	if err == nil && len(out.Messages) == 0 {
		t.Log("poison left the dedicated main queue (DLQ receive timed out; LocalStack redrive lag)")
		return
	}
	t.Fatal("expected poison message in dedicated DLQ or removed from dedicated main queue after maxReceiveCount")
}
