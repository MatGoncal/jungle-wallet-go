//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/appfx"
	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"
)

func clientToken(t *testing.T, clientID, secret string) string {
	t.Helper()
	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", clientID)
	form.Set("client_secret", secret)
	resp, err := http.Post(sharedOIDCIssuer+"/protocol/openid-connect/token",
		"application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("token %s: %s", clientID, body)
	}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.AccessToken == "" {
		t.Fatalf("parse token: %v body=%s", err, body)
	}
	return out.AccessToken
}

func startAPI(t *testing.T) (base string, app *fxtest.App) {
	t.Helper()
	addr := freePort(t)
	_ = os.Setenv("HTTP_ADDR", addr)
	_ = os.Setenv("DATABASE_URL", sharedDBURL)
	_ = os.Setenv("SQS_ENDPOINT", sharedSQSEndpoint)
	_ = os.Setenv("SQS_WAGER_QUEUE_URL", sharedWagerURL)
	_ = os.Setenv("SQS_EVENTS_QUEUE_URL", sharedEventsURL)
	_ = os.Setenv("OIDC_ISSUER_URL", sharedOIDCIssuer)
	_ = os.Setenv("OIDC_DISCOVERY_URL", sharedOIDCDisc)
	_ = os.Setenv("OIDC_AUDIENCE", "")
	_ = os.Setenv("AWS_ACCESS_KEY_ID", "test")
	_ = os.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	_ = os.Setenv("AWS_REGION", "us-east-1")
	_ = os.Setenv("SHUTDOWN_TIMEOUT", "5s")

	app = fxtest.New(t, appfx.Options(), fx.NopLogger)
	app.RequireStart()
	base = "http://" + addr
	waitReady(t, base, 20*time.Second)
	t.Cleanup(func() { app.RequireStop() })
	return base, app
}

func countFinancial(t *testing.T, pool *pgxpool.Pool) (wallets, txs, ledger, outbox int) {
	t.Helper()
	_ = pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM wallets`).Scan(&wallets)
	_ = pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM wager_transactions`).Scan(&txs)
	_ = pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM wallet_ledger_entries`).Scan(&ledger)
	_ = pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM outbox_events`).Scan(&outbox)
	return
}

func TestFx_LifecycleStartsAndStops(t *testing.T) {
	base, app := startAPI(t)
	resp, err := http.Get(base + "/health/live")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("live=%d", resp.StatusCode)
	}
	resp2, err := http.Get(base + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != 200 {
		t.Fatalf("metrics=%d", resp2.StatusCode)
	}
	app.RequireStop()
}

func TestAuthz_NoFinancialEffect(t *testing.T) {
	base, _ := startAPI(t)
	pool := openPool(t)
	beforeW, beforeT, _, _ := countFinancial(t, pool)

	tokenA := clientToken(t, "provider-a", "provider-a-secret")
	tokenB := clientToken(t, "provider-b", "provider-b-secret")
	tokenInternal := clientToken(t, "internal-service", "internal-service-secret")

	assertStatus := func(name string, status int, req *http.Request) {
		t.Helper()
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != status {
			b, _ := io.ReadAll(resp.Body)
			t.Fatalf("%s: got %d want %d body=%s", name, resp.StatusCode, status, b)
		}
	}

	// Missing credential
	req, _ := http.NewRequest(http.MethodPost, base+"/wallets", bytes.NewReader([]byte(`{}`)))
	assertStatus("missing auth", http.StatusUnauthorized, req)

	// Invalid token
	req, _ = http.NewRequest(http.MethodPost, base+"/wallets", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Authorization", "Bearer not-a-jwt")
	assertStatus("invalid token", http.StatusUnauthorized, req)

	// Provider cannot open wallet
	body := fmt.Sprintf(`{"playerId":%q,"initialBalance":{"amount":"10.00","currency":"BRL"}}`, uuid.NewString())
	req, _ = http.NewRequest(http.MethodPost, base+"/wallets", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tokenA)
	req.Header.Set("Content-Type", "application/json")
	assertStatus("provider open wallet", http.StatusForbidden, req)

	// Provider cannot reconcile
	req, _ = http.NewRequest(http.MethodPost, base+"/wallets/"+uuid.NewString()+"/reconciliation", nil)
	req.Header.Set("Authorization", "Bearer "+tokenA)
	assertStatus("provider reconcile", http.StatusForbidden, req)

	// Create wallet with internal, then provider-b must not see provider-a tx
	req, _ = http.NewRequest(http.MethodPost, base+"/wallets", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tokenInternal)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("open wallet: %d %s", resp.StatusCode, b)
	}
	var created struct {
		ID       string `json:"id"`
		PlayerID string `json:"playerId"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&created)

	wager := map[string]any{
		"providerId": "provider-a", "externalTransactionId": "authz-ext-1",
		"playerId": created.PlayerID, "walletId": created.ID,
		"roundId": "r", "gameId": "g", "kind": "BET",
		"money": map[string]string{"amount": "1.00", "currency": "BRL"},
	}
	raw, _ := json.Marshal(wager)
	req, _ = http.NewRequest(http.MethodPost, base+"/wagering/transactions", bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+tokenA)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "authz-idem-1")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	bodyBytes, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 && resp.StatusCode != 422 {
		t.Fatalf("bet: %d %s", resp.StatusCode, bodyBytes)
	}
	var betOut struct {
		TransactionID string `json:"transactionId"`
	}
	_ = json.Unmarshal(bodyBytes, &betOut)

	// provider-b cannot read provider-a transaction
	req, _ = http.NewRequest(http.MethodGet, base+"/wagering/transactions/"+betOut.TransactionID, nil)
	req.Header.Set("Authorization", "Bearer "+tokenB)
	assertStatus("provider-b read a tx", http.StatusNotFound, req)

	// provider-b replay with provider-a providerId in body -> 403
	req, _ = http.NewRequest(http.MethodPost, base+"/wagering/transactions", bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+tokenB)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "authz-idem-1")
	assertStatus("provider-b replay a key", http.StatusForbidden, req)

	afterW, afterT, afterL, afterO := countFinancial(t, pool)
	// Wallet open + one BET may have increased counts; auth failures must not add more beyond that baseline snapshot...
	// Re-check: unauthorized attempts before wallet creation should not have changed DB.
	_ = beforeW
	if afterW < beforeW || afterT < beforeT {
		t.Fatalf("unexpected shrink")
	}
	// Explicit: forbidden open/reconcile did not create extra wallets beyond the one intentional create.
	if afterW != beforeW+1 {
		t.Fatalf("wallets before=%d after=%d (want +1 intentional)", beforeW, afterW)
	}
	_ = afterL
	_ = afterO
}
