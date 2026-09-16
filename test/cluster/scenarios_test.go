//go:build cluster

package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/app"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/infra/postgres"
)

var (
	dbURL       string
	sqsEndpoint string
	wagerURL    string
	eventsURL   string
	oidcIssuer  string
	apiBases    []string
	apiBin      string
	apiProcs    []*exec.Cmd
)

func TestMain(m *testing.M) {
	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "../.."))

	dbURL = getenv("DATABASE_URL", "postgres://wallet_app:wallet_app@localhost:55432/wallet?sslmode=disable")
	sqsEndpoint = getenv("SQS_ENDPOINT", "http://localhost:4566")
	wagerURL = getenv("SQS_WAGER_QUEUE_URL", sqsEndpoint+"/000000000000/wager-transactions.fifo")
	eventsURL = getenv("SQS_EVENTS_QUEUE_URL", sqsEndpoint+"/000000000000/wallet-events.fifo")
	oidcIssuer = getenv("OIDC_ISSUER_URL", "http://localhost:8081/realms/jungle-wallet")

	if os.Getenv("CLUSTER_API_BASES") != "" {
		apiBases = strings.Split(os.Getenv("CLUSTER_API_BASES"), ",")
	} else {
		bin := filepath.Join(os.TempDir(), "jungle-wallet-api-cluster")
		build := exec.Command("go", "build", "-o", bin, "./cmd/api")
		build.Dir = repoRoot
		build.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := build.CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "build api: %v\n%s\n", err, out)
			os.Exit(1)
		}
		apiBin = bin
		for i := 0; i < 3; i++ {
			addr := freeListenAddr()
			cmd := exec.Command(bin)
			cmd.Env = append(os.Environ(),
				"HTTP_ADDR="+addr,
				"DATABASE_URL="+dbURL,
				"SQS_ENDPOINT="+sqsEndpoint,
				"SQS_WAGER_QUEUE_URL="+wagerURL,
				"SQS_EVENTS_QUEUE_URL="+eventsURL,
				"OIDC_ISSUER_URL="+oidcIssuer,
				"OIDC_DISCOVERY_URL="+getenv("OIDC_DISCOVERY_URL", oidcIssuer),
				"OIDC_AUDIENCE=",
				"AWS_ACCESS_KEY_ID=test",
				"AWS_SECRET_ACCESS_KEY=test",
				"AWS_REGION=us-east-1",
				"SHUTDOWN_TIMEOUT=5s",
			)
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			if err := cmd.Start(); err != nil {
				fmt.Fprintf(os.Stderr, "start api: %v\n", err)
				os.Exit(1)
			}
			apiProcs = append(apiProcs, cmd)
			apiBases = append(apiBases, "http://"+addr)
		}
		for _, base := range apiBases {
			if err := waitHTTP(base+"/health/live", 45*time.Second); err != nil {
				fmt.Fprintf(os.Stderr, "api ready: %v\n", err)
				cleanupProcs()
				os.Exit(1)
			}
		}
	}

	code := m.Run()
	cleanupProcs()
	os.Exit(code)
}

func cleanupProcs() {
	for _, p := range apiProcs {
		if p != nil && p.Process != nil {
			_ = p.Process.Signal(os.Interrupt)
			done := make(chan error, 1)
			go func(c *exec.Cmd) { done <- c.Wait() }(p)
			select {
			case <-done:
			case <-time.After(8 * time.Second):
				_ = p.Process.Kill()
			}
		}
	}
}

func getenv(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func freeListenAddr() string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

func waitHTTP(url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == 200 {
				return nil
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for %s", url)
}

func pool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	p, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}

func token(t *testing.T, clientID, secret string) string {
	t.Helper()
	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", clientID)
	form.Set("client_secret", secret)
	resp, err := http.Post(oidcIssuer+"/protocol/openid-connect/token",
		"application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("token: %s", b)
	}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	_ = json.Unmarshal(b, &out)
	return out.AccessToken
}

func openWallet(t *testing.T, base, internalToken string, amount string) (walletID, playerID string) {
	t.Helper()
	playerID = uuid.NewString()
	body := fmt.Sprintf(`{"playerId":%q,"initialBalance":{"amount":%q,"currency":"BRL"}}`, playerID, amount)
	req, _ := http.NewRequest(http.MethodPost, base+"/wallets", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+internalToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 201 {
		t.Fatalf("open wallet: %d %s", resp.StatusCode, raw)
	}
	var out struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(raw, &out)
	return out.ID, playerID
}

func postBet(t *testing.T, base, providerToken, walletID, playerID, ext, idem, amount string) (status int, body map[string]any) {
	t.Helper()
	payload := map[string]any{
		"providerId": "provider-a", "externalTransactionId": ext,
		"playerId": playerID, "walletId": walletID, "roundId": "r", "gameId": "g",
		"kind": "BET", "money": map[string]string{"amount": amount, "currency": "BRL"},
	}
	raw, _ := json.Marshal(payload)
	req, _ := http.NewRequest(http.MethodPost, base+"/wagering/transactions", bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+providerToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", idem)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(b, &body)
	return resp.StatusCode, body
}

func walletBalance(t *testing.T, p *pgxpool.Pool, walletID string) int64 {
	t.Helper()
	var bal int64
	if err := p.QueryRow(context.Background(), `SELECT balance_minor FROM wallets WHERE id=$1`, walletID).Scan(&bal); err != nil {
		t.Fatal(err)
	}
	return bal
}

func ledgerDebits(t *testing.T, p *pgxpool.Pool, walletID string) int {
	t.Helper()
	var n int
	_ = p.QueryRow(context.Background(), `
		SELECT COUNT(*) FROM wallet_ledger_entries WHERE wallet_id=$1 AND direction='DEBIT'`, walletID).Scan(&n)
	return n
}

func assertBalanceEqualsLedger(t *testing.T, p *pgxpool.Pool, walletID string) {
	t.Helper()
	var stored, credits, debits int64
	err := p.QueryRow(context.Background(), `
		SELECT w.balance_minor,
		       COALESCE(SUM(CASE WHEN l.direction='CREDIT' THEN l.amount_minor ELSE 0 END),0),
		       COALESCE(SUM(CASE WHEN l.direction='DEBIT' THEN l.amount_minor ELSE 0 END),0)
		FROM wallets w
		LEFT JOIN wallet_ledger_entries l ON l.wallet_id = w.id
		WHERE w.id = $1
		GROUP BY w.balance_minor`, walletID).Scan(&stored, &credits, &debits)
	if err != nil {
		t.Fatal(err)
	}
	if stored != credits-debits {
		t.Fatalf("wallet %s stored=%d credits-debits=%d", walletID, stored, credits-debits)
	}
}

func TestCluster_SameBet50Parallel_OneDebit(t *testing.T) {
	p := pool(t)
	internal := token(t, "internal-service", "internal-service-secret")
	provider := token(t, "provider-a", "provider-a-secret")
	walletID, playerID := openWallet(t, apiBases[0], internal, "100.00")
	runID := uuid.NewString()
	ext, idem := "same-bet-50-"+runID, "same-idem-50-"+runID

	var wg sync.WaitGroup
	var replays atomic.Int64
	var processed atomic.Int64
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			base := apiBases[i%len(apiBases)]
			st, body := postBet(t, base, provider, walletID, playerID, ext, idem, "10.00")
			if st != 200 {
				t.Errorf("status=%d body=%v", st, body)
				return
			}
			if v, _ := body["idempotentReplay"].(bool); v {
				replays.Add(1)
			} else {
				processed.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if processed.Load() != 1 || replays.Load() != 49 {
		t.Fatalf("processed=%d replays=%d", processed.Load(), replays.Load())
	}
	if ledgerDebits(t, p, walletID) != 1 {
		t.Fatalf("debits=%d", ledgerDebits(t, p, walletID))
	}
	assertBalanceEqualsLedger(t, p, walletID)
}

func TestCluster_TwoBets80On100(t *testing.T) {
	p := pool(t)
	internal := token(t, "internal-service", "internal-service-secret")
	provider := token(t, "provider-a", "provider-a-secret")
	walletID, playerID := openWallet(t, apiBases[0], internal, "100.00")
	runID := uuid.NewString()

	var wg sync.WaitGroup
	results := make(chan int, 2)
	for i, ext := range []string{"bet-80-a-" + runID, "bet-80-b-" + runID} {
		wg.Add(1)
		go func(i int, ext string) {
			defer wg.Done()
			st, _ := postBet(t, apiBases[i%len(apiBases)], provider, walletID, playerID, ext, "idem-"+ext, "80.00")
			results <- st
		}(i, ext)
	}
	wg.Wait()
	close(results)
	ok, rejected := 0, 0
	other := []int{}
	for st := range results {
		switch st {
		case 200:
			ok++
		case 422:
			rejected++
		default:
			other = append(other, st)
		}
	}
	if ok != 1 || rejected != 1 {
		t.Fatalf("ok=%d rejected=%d other=%v bal=%d", ok, rejected, other, walletBalance(t, p, walletID))
	}
	if walletBalance(t, p, walletID) != 2000 {
		t.Fatalf("balance=%d want 2000", walletBalance(t, p, walletID))
	}
	if ledgerDebits(t, p, walletID) != 1 {
		t.Fatalf("debits=%d", ledgerDebits(t, p, walletID))
	}
	assertBalanceEqualsLedger(t, p, walletID)
}

func TestCluster_DistinctWalletsParallel(t *testing.T) {
	internal := token(t, "internal-service", "internal-service-secret")
	provider := token(t, "provider-a", "provider-a-secret")
	runID := uuid.NewString()
	const n = 8
	type pair struct{ w, p string }
	wallets := make([]pair, n)
	for i := 0; i < n; i++ {
		w, p := openWallet(t, apiBases[i%len(apiBases)], internal, "50.00")
		wallets[i] = pair{w, p}
	}
	start := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ext := fmt.Sprintf("par-%s-%d", runID, i)
			st, _ := postBet(t, apiBases[i%len(apiBases)], provider, wallets[i].w, wallets[i].p, ext, "idem-"+ext, "5.00")
			if st != 200 {
				t.Errorf("wallet %d status=%d", i, st)
			}
		}(i)
	}
	wg.Wait()
	elapsed := time.Since(start)
	if elapsed > 15*time.Second {
		t.Fatalf("parallel wallets took too long: %s", elapsed)
	}
	p := pool(t)
	for _, w := range wallets {
		assertBalanceEqualsLedger(t, p, w.w)
	}
}

func TestCluster_ThreeInstancesSimultaneously(t *testing.T) {
	if len(apiBases) < 3 {
		t.Skip("need 3 api bases")
	}
	p := pool(t)
	internal := token(t, "internal-service", "internal-service-secret")
	provider := token(t, "provider-a", "provider-a-secret")
	walletID, playerID := openWallet(t, apiBases[0], internal, "100.00")
	runID := uuid.NewString()
	ext, idem := "tri-instance-"+runID, "tri-idem-"+runID
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			base := apiBases[i%3]
			_, _ = postBet(t, base, provider, walletID, playerID, ext, idem, "5.00")
		}(i)
	}
	wg.Wait()
	if ledgerDebits(t, p, walletID) != 1 {
		t.Fatalf("debits=%d", ledgerDebits(t, p, walletID))
	}
	assertBalanceEqualsLedger(t, p, walletID)
}

func TestCluster_KillBetweenCommitAndDelete_Replay(t *testing.T) {
	p := pool(t)
	internal := token(t, "internal-service", "internal-service-secret")
	provider := token(t, "provider-a", "provider-a-secret")
	walletID, playerID := openWallet(t, apiBases[0], internal, "100.00")
	runID := uuid.NewString()
	ext, idem := "kill-ext-"+runID, "kill-idem-"+runID

	st, body := postBet(t, apiBases[0], provider, walletID, playerID, ext, idem, "10.00")
	if st != 200 {
		t.Fatalf("first bet: %d %v", st, body)
	}

	ctx := context.Background()
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion("us-east-1"),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")),
	)
	if err != nil {
		t.Fatal(err)
	}
	sqsClient := awssqs.NewFromConfig(awsCfg, func(o *awssqs.Options) {
		o.BaseEndpoint = aws.String(sqsEndpoint)
	})
	msgID := "kill-msg-" + runID
	env := map[string]any{
		"messageId": msgID, "type": "WagerTransactionRequested", "occurredAt": time.Now().UTC().Format(time.RFC3339Nano),
		"data": map[string]any{
			"providerId": "provider-a", "externalTransactionId": ext, "idempotencyKey": idem,
			"playerId": playerID, "walletId": walletID, "roundId": "r", "gameId": "g", "kind": "BET",
			"money": map[string]string{"amount": "10.00", "currency": "BRL"},
		},
	}
	raw, _ := json.Marshal(env)
	_, err = sqsClient.SendMessage(ctx, &awssqs.SendMessageInput{
		QueueUrl: aws.String(wagerURL), MessageBody: aws.String(string(raw)),
		MessageGroupId: aws.String(walletID), MessageDeduplicationId: aws.String(idem + "-sqs"),
	})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if ledgerDebits(t, p, walletID) == 1 && walletBalance(t, p, walletID) == 9000 {
			assertBalanceEqualsLedger(t, p, walletID)
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("expected single debit after SQS redelivery replay, bal=%d debits=%d",
		walletBalance(t, p, walletID), ledgerDebits(t, p, walletID))
}

func TestCluster_HTTPAndSQSSameOperation(t *testing.T) {
	p := pool(t)
	internal := token(t, "internal-service", "internal-service-secret")
	provider := token(t, "provider-a", "provider-a-secret")
	walletID, playerID := openWallet(t, apiBases[1], internal, "100.00")
	runID := uuid.NewString()
	ext, idem := "cross-ext-"+runID, "cross-idem-"+runID

	ctx := context.Background()
	awsCfg, _ := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion("us-east-1"),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")),
	)
	sqsClient := awssqs.NewFromConfig(awsCfg, func(o *awssqs.Options) {
		o.BaseEndpoint = aws.String(sqsEndpoint)
	})
	msg := map[string]any{
		"messageId": "cross-msg-" + runID, "type": "WagerTransactionRequested", "occurredAt": time.Now().UTC().Format(time.RFC3339Nano),
		"data": map[string]any{
			"providerId": "provider-a", "externalTransactionId": ext, "idempotencyKey": idem,
			"playerId": playerID, "walletId": walletID, "roundId": "r", "gameId": "g", "kind": "BET",
			"money": map[string]string{"amount": "7.00", "currency": "BRL"},
		},
	}
	raw, _ := json.Marshal(msg)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = postBet(t, apiBases[0], provider, walletID, playerID, ext, idem, "7.00")
	}()
	go func() {
		defer wg.Done()
		_, _ = sqsClient.SendMessage(ctx, &awssqs.SendMessageInput{
			QueueUrl: aws.String(wagerURL), MessageBody: aws.String(string(raw)),
			MessageGroupId: aws.String(walletID), MessageDeduplicationId: aws.String(idem),
		})
	}()
	wg.Wait()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if ledgerDebits(t, p, walletID) == 1 {
			assertBalanceEqualsLedger(t, p, walletID)
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("cross HTTP/SQS produced debits=%d", ledgerDebits(t, p, walletID))
}

func TestCluster_RefundBeforeReference_ThenExpire(t *testing.T) {
	p := pool(t)
	internal := token(t, "internal-service", "internal-service-secret")
	provider := token(t, "provider-a", "provider-a-secret")
	walletID, playerID := openWallet(t, apiBases[0], internal, "100.00")
	runID := uuid.NewString()

	payload := map[string]any{
		"providerId": "provider-a", "externalTransactionId": "early-refund-" + runID,
		"playerId": playerID, "walletId": walletID, "roundId": "r", "gameId": "g",
		"kind": "REFUND", "money": map[string]string{"amount": "10.00", "currency": "BRL"},
		"referenceExternalTransactionId": "bet-not-yet-" + runID,
	}
	raw, _ := json.Marshal(payload)
	req, _ := http.NewRequest(http.MethodPost, apiBases[0]+"/wagering/transactions", bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+provider)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "early-refund-idem-"+runID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202 pending reference, got %d %s", resp.StatusCode, b)
	}
	var out struct {
		TransactionID string `json:"transactionId"`
	}
	_ = json.Unmarshal(b, &out)
	if out.TransactionID == "" {
		t.Fatalf("missing transactionId in %s", b)
	}

	_, err = p.Exec(context.Background(), `
		UPDATE reference_retry_state
		SET next_attempt_at = NOW() - interval '1 second',
		    expires_at = NOW() - interval '1 second',
		    attempts = 20
		WHERE transaction_id = $1::uuid`, out.TransactionID)
	if err != nil {
		t.Fatal(err)
	}

	uow := postgres.NewUnitOfWork(p)
	resolve := app.NewResolvePendingReference(uow)
	txID, err := uuid.Parse(out.TransactionID)
	if err != nil {
		t.Fatal(err)
	}
	if err := resolve.Execute(context.Background(), app.ResolvePendingInput{
		TransactionID: txID, CorrelationID: "cluster-expire", ReferenceMaxAttempts: 2,
	}); err != nil {
		t.Fatal(err)
	}

	var status, code string
	if err := p.QueryRow(context.Background(), `
		SELECT status, COALESCE(failure_code,'') FROM wager_transactions WHERE id=$1::uuid`, out.TransactionID).Scan(&status, &code); err != nil {
		t.Fatal(err)
	}
	if status != "REJECTED" || code != "REFERENCE_NOT_FOUND" {
		t.Fatalf("status=%s code=%s", status, code)
	}
	assertBalanceEqualsLedger(t, p, walletID)
}

func TestCluster_RestartPreservesConsistency(t *testing.T) {
	if apiBin == "" || len(apiProcs) < 1 {
		t.Skip("requires locally spawned api processes")
	}
	p := pool(t)
	internal := token(t, "internal-service", "internal-service-secret")
	provider := token(t, "provider-a", "provider-a-secret")
	walletID, playerID := openWallet(t, apiBases[0], internal, "100.00")
	runID := uuid.NewString()
	ext, idem := "restart-ext-"+runID, "restart-idem-"+runID
	_, _ = postBet(t, apiBases[0], provider, walletID, playerID, ext, idem, "10.00")

	old := apiProcs[0]
	addr := strings.TrimPrefix(apiBases[0], "http://")
	_ = old.Process.Signal(os.Interrupt)
	_, _ = old.Process.Wait()

	cmd := exec.Command(apiBin)
	cmd.Env = append(os.Environ(),
		"HTTP_ADDR="+addr,
		"DATABASE_URL="+dbURL,
		"SQS_ENDPOINT="+sqsEndpoint,
		"SQS_WAGER_QUEUE_URL="+wagerURL,
		"SQS_EVENTS_QUEUE_URL="+eventsURL,
		"OIDC_ISSUER_URL="+oidcIssuer,
		"OIDC_DISCOVERY_URL="+getenv("OIDC_DISCOVERY_URL", oidcIssuer),
		"OIDC_AUDIENCE=",
		"AWS_ACCESS_KEY_ID=test",
		"AWS_SECRET_ACCESS_KEY=test",
		"AWS_REGION=us-east-1",
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	apiProcs[0] = cmd
	if err := waitHTTP(apiBases[0]+"/health/live", 30*time.Second); err != nil {
		t.Fatal(err)
	}

	st, body := postBet(t, apiBases[0], provider, walletID, playerID, ext, idem, "10.00")
	if st != 200 {
		t.Fatalf("replay after restart: %d %v", st, body)
	}
	if replay, _ := body["idempotentReplay"].(bool); !replay {
		t.Fatal("expected idempotent replay after restart")
	}
	if ledgerDebits(t, p, walletID) != 1 {
		t.Fatalf("debits=%d", ledgerDebits(t, p, walletID))
	}
	assertBalanceEqualsLedger(t, p, walletID)
}

func TestCluster_FinalBalanceEqualsCreditsMinusDebits(t *testing.T) {
	p := pool(t)
	internal := token(t, "internal-service", "internal-service-secret")
	provider := token(t, "provider-a", "provider-a-secret")
	walletID, playerID := openWallet(t, apiBases[0], internal, "100.00")
	runID := uuid.NewString()
	_, _ = postBet(t, apiBases[0], provider, walletID, playerID, "final-bet-"+runID, "final-idem-"+runID, "25.00")
	assertBalanceEqualsLedger(t, p, walletID)
	if walletBalance(t, p, walletID) != 7500 {
		t.Fatalf("balance=%d", walletBalance(t, p, walletID))
	}
}
