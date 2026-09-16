//go:build integration

package integration

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/app"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/money"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/wallet"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/infra/postgres"
)

func TestMigrations_UpDownUp(t *testing.T) {
	if sharedPool != nil {
		sharedPool.Close()
		sharedPool = nil
	}
	if err := migrateDown(sharedMigrateURL); err != nil {
		t.Fatalf("down: %v", err)
	}
	if err := migrateUp(sharedMigrateURL); err != nil {
		t.Fatalf("up again: %v", err)
	}
	pool := openPool(t)
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM information_schema.tables WHERE table_name = 'wallets'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("wallets table missing after re-migrate, n=%d", n)
	}
}

func TestConstraints_BalanceNonNegative(t *testing.T) {
	pool := openPool(t)
	id, player := mustV7(t), mustV7(t)
	_, err := pool.Exec(context.Background(), `
		INSERT INTO wallets (id, player_id, currency, balance_minor, version, created_at, updated_at)
		VALUES ($1,$2,'BRL',-1,1,NOW(),NOW())`, id, player)
	if err == nil {
		t.Fatal("expected CHECK wallets_balance_non_negative to fail")
	}
}

func TestConstraints_PlayerCurrencyUnique(t *testing.T) {
	pool := openPool(t)
	player := mustV7(t)
	insertWallet(t, pool, mustV7(t), player, 0)
	_, err := pool.Exec(context.Background(), `
		INSERT INTO wallets (id, player_id, currency, balance_minor, version, created_at, updated_at)
		VALUES ($1,$2,'BRL',0,1,NOW(),NOW())`, mustV7(t), player)
	if err == nil {
		t.Fatal("expected UNIQUE (player_id, currency) to fail")
	}
}

func TestConstraints_ExternalUniques(t *testing.T) {
	pool := openPool(t)
	walletID, player := mustV7(t), mustV7(t)
	insertWallet(t, pool, walletID, player, 10000)

	insertExternal := func(id uuid.UUID, ext, key string) error {
		_, err := pool.Exec(context.Background(), `
			INSERT INTO wager_transactions (
				id, origin, provider_id, external_transaction_id, idempotency_key, payload_hash,
				wallet_id, player_id, round_id, game_id, kind, amount_minor, currency, status, created_at, updated_at
			) VALUES ($1,'EXTERNAL','provider-a',$2,$3,'hash',$4,$5,'r1','g1','BET',100,'BRL','PROCESSED',NOW(),NOW())`,
			id, ext, key, walletID, player)
		return err
	}

	if err := insertExternal(mustV7(t), "ext-1", "key-1"); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if err := insertExternal(mustV7(t), "ext-1", "key-2"); err == nil {
		t.Fatal("expected unique (provider, external_transaction_id)")
	}
	if err := insertExternal(mustV7(t), "ext-2", "key-1"); err == nil {
		t.Fatal("expected unique (provider, idempotency_key)")
	}
}

func TestConstraints_OneOpeningPerWallet(t *testing.T) {
	pool := openPool(t)
	walletID, player := mustV7(t), mustV7(t)
	insertWallet(t, pool, walletID, player, 10000)

	insertOpening := func(id uuid.UUID) error {
		_, err := pool.Exec(context.Background(), `
			INSERT INTO wager_transactions (
				id, origin, wallet_id, player_id, kind, amount_minor, currency, status, created_at, updated_at
			) VALUES ($1,'INTERNAL',$2,$3,'OPENING',10000,'BRL','PROCESSED',NOW(),NOW())`, id, walletID, player)
		return err
	}
	if err := insertOpening(mustV7(t)); err != nil {
		t.Fatalf("first opening: %v", err)
	}
	if err := insertOpening(mustV7(t)); err == nil {
		t.Fatal("expected one OPENING per wallet")
	}
}

func TestConstraints_OpeningShape(t *testing.T) {
	pool := openPool(t)
	walletID, player := mustV7(t), mustV7(t)
	insertWallet(t, pool, walletID, player, 0)
	_, err := pool.Exec(context.Background(), `
		INSERT INTO wager_transactions (
			id, origin, provider_id, wallet_id, player_id, kind, amount_minor, currency, status, created_at, updated_at
		) VALUES ($1,'INTERNAL','provider-a',$2,$3,'OPENING',100,'BRL','PROCESSED',NOW(),NOW())`,
		mustV7(t), walletID, player)
	if err == nil {
		t.Fatal("expected OPENING shape check to reject provider_id")
	}
}

func TestConstraints_LedgerMathAndAppendOnly(t *testing.T) {
	pool := openPool(t)
	walletID, player, txID, entryID := mustV7(t), mustV7(t), mustV7(t), mustV7(t)
	insertWallet(t, pool, walletID, player, 10000)

	_, err := pool.Exec(context.Background(), `
		INSERT INTO wager_transactions (
			id, origin, provider_id, external_transaction_id, idempotency_key, payload_hash,
			wallet_id, player_id, round_id, game_id, kind, amount_minor, currency, status, created_at, updated_at
		) VALUES ($1,'EXTERNAL','provider-a',$2,$3,'h',$4,$5,'r','g','BET',1000,'BRL','PROCESSED',NOW(),NOW())`,
		txID, "ext-ledger", "key-ledger", walletID, player)
	if err != nil {
		t.Fatalf("tx: %v", err)
	}

	_, err = pool.Exec(context.Background(), `
		INSERT INTO wallet_ledger_entries (
			id, wallet_id, transaction_id, direction, amount_minor, currency, balance_before, balance_after, created_at
		) VALUES ($1,$2,$3,'DEBIT',1000,'BRL',10000,9999,NOW())`, entryID, walletID, txID)
	if err == nil {
		t.Fatal("expected ledger_balance_math to fail")
	}

	_, err = pool.Exec(context.Background(), `
		INSERT INTO wallet_ledger_entries (
			id, wallet_id, transaction_id, direction, amount_minor, currency, balance_before, balance_after, created_at
		) VALUES ($1,$2,$3,'DEBIT',1000,'BRL',10000,9000,NOW())`, entryID, walletID, txID)
	if err != nil {
		t.Fatalf("valid ledger insert: %v", err)
	}

	_, err = pool.Exec(context.Background(), `
		UPDATE wallet_ledger_entries SET amount_minor = 1 WHERE id = $1`, entryID)
	if err == nil {
		t.Fatal("expected append-only trigger on UPDATE")
	}

	_, err = pool.Exec(context.Background(), `
		DELETE FROM wallet_ledger_entries WHERE id = $1`, entryID)
	if err == nil {
		t.Fatal("expected append-only trigger on DELETE")
	}

	// duplicate (wallet_id, transaction_id)
	_, err = pool.Exec(context.Background(), `
		INSERT INTO wallet_ledger_entries (
			id, wallet_id, transaction_id, direction, amount_minor, currency, balance_before, balance_after, created_at
		) VALUES ($1,$2,$3,'DEBIT',1000,'BRL',10000,9000,NOW())`, mustV7(t), walletID, txID)
	if err == nil {
		t.Fatal("expected unique (wallet_id, transaction_id)")
	}
}

// TestLedger_AppRoleCannotBypassAppendOnly proves the second defense layer:
// wallet_app is not table owner (cannot DROP the trigger) and has no UPDATE privilege
// (SQLSTATE 42501), so the append-only rule is not only a trigger the app could remove.
func TestLedger_AppRoleCannotBypassAppendOnly(t *testing.T) {
	appPool := openPool(t)
	var role string
	if err := appPool.QueryRow(context.Background(), `SELECT current_user`).Scan(&role); err != nil {
		t.Fatal(err)
	}
	if role != "wallet_app" {
		t.Fatalf("expected current_user=wallet_app, got %q (DATABASE_URL must use the app role)", role)
	}

	walletID, player, txID, entryID := mustV7(t), mustV7(t), mustV7(t), mustV7(t)
	insertWallet(t, appPool, walletID, player, 10000)
	_, err := appPool.Exec(context.Background(), `
		INSERT INTO wager_transactions (
			id, origin, provider_id, external_transaction_id, idempotency_key, payload_hash,
			wallet_id, player_id, round_id, game_id, kind, amount_minor, currency, status, created_at, updated_at
		) VALUES ($1,'EXTERNAL','provider-a',$2,$3,'h',$4,$5,'r','g','BET',1000,'BRL','PROCESSED',NOW(),NOW())`,
		txID, "ext-app-role", "key-app-role", walletID, player)
	if err != nil {
		t.Fatalf("tx: %v", err)
	}
	_, err = appPool.Exec(context.Background(), `
		INSERT INTO wallet_ledger_entries (
			id, wallet_id, transaction_id, direction, amount_minor, currency, balance_before, balance_after, created_at
		) VALUES ($1,$2,$3,'DEBIT',1000,'BRL',10000,9000,NOW())`, entryID, walletID, txID)
	if err != nil {
		t.Fatalf("ledger insert (allowed): %v", err)
	}

	_, err = appPool.Exec(context.Background(), `
		DROP TRIGGER IF EXISTS wallet_ledger_append_only ON wallet_ledger_entries`)
	if err == nil {
		t.Fatal("wallet_app must not be able to DROP the append-only trigger")
	}

	_, err = appPool.Exec(context.Background(), `
		UPDATE wallet_ledger_entries SET amount_minor = 1 WHERE id = $1`, entryID)
	if err == nil {
		t.Fatal("expected UPDATE to fail for wallet_app")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
		t.Fatalf("expected permission denied (42501), got %v", err)
	}
}

func TestConstraints_OneReversalPerReference(t *testing.T) {
	pool := openPool(t)
	walletID, player := mustV7(t), mustV7(t)
	betID := mustV7(t)
	insertWallet(t, pool, walletID, player, 10000)

	_, err := pool.Exec(context.Background(), `
		INSERT INTO wager_transactions (
			id, origin, provider_id, external_transaction_id, idempotency_key, payload_hash,
			wallet_id, player_id, round_id, game_id, kind, amount_minor, currency, status, created_at, updated_at
		) VALUES ($1,'EXTERNAL','provider-a','bet-1','k-bet','h',$2,$3,'r','g','BET',1000,'BRL','PROCESSED',NOW(),NOW())`,
		betID, walletID, player)
	if err != nil {
		t.Fatalf("bet: %v", err)
	}

	insertReversal := func(id uuid.UUID, kind, ext, key string) error {
		_, err := pool.Exec(context.Background(), `
			INSERT INTO wager_transactions (
				id, origin, provider_id, external_transaction_id, idempotency_key, payload_hash,
				wallet_id, player_id, round_id, game_id, kind, amount_minor, currency,
				reference_external_transaction_id, reference_transaction_id, status, created_at, updated_at
			) VALUES ($1,'EXTERNAL','provider-a',$2,$3,'h',$4,$5,'r','g',$6,1000,'BRL','bet-1',$7,'PROCESSED',NOW(),NOW())`,
			id, ext, key, walletID, player, kind, betID)
		return err
	}

	if err := insertReversal(mustV7(t), "REFUND", "ref-1", "k-ref-1"); err != nil {
		t.Fatalf("first reversal: %v", err)
	}
	if err := insertReversal(mustV7(t), "ROLLBACK", "ref-2", "k-ref-2"); err == nil {
		t.Fatal("expected one PROCESSED reversal per reference_transaction_id")
	}
}

func TestConstraints_InboxUnique(t *testing.T) {
	pool := openPool(t)
	id1, id2 := mustV7(t), mustV7(t)
	_, err := pool.Exec(context.Background(), `
		INSERT INTO inbox_messages (id, consumer_name, message_id, payload_hash, received_at)
		VALUES ($1,'wager-consumer','msg-1','h',NOW())`, id1)
	if err != nil {
		t.Fatalf("first inbox: %v", err)
	}
	_, err = pool.Exec(context.Background(), `
		INSERT INTO inbox_messages (id, consumer_name, message_id, payload_hash, received_at)
		VALUES ($1,'wager-consumer','msg-1','h2',NOW())`, id2)
	if err == nil {
		t.Fatal("expected unique (consumer_name, message_id)")
	}
}

func TestRepositories_UnitOfWorkCommitAndRollback(t *testing.T) {
	pool := openPool(t)
	uow := postgres.NewUnitOfWork(pool)
	ctx := context.Background()

	walletID, playerID := mustV7(t), mustV7(t)
	now := time.Now().UTC()
	bal, err := money.FromMinor(5000, "BRL")
	if err != nil {
		t.Fatal(err)
	}
	w, err := wallet.Create(walletID, playerID, bal, now)
	if err != nil {
		t.Fatal(err)
	}

	err = uow.WithinTransaction(ctx, func(ctx context.Context, repos app.Repositories) error {
		if err := repos.Wallets().Insert(ctx, w); err != nil {
			return err
		}
		return fmt.Errorf("force rollback")
	})
	if err == nil {
		t.Fatal("expected error")
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM wallets WHERE id = $1`, walletID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("wallet should have rolled back, count=%d", count)
	}

	err = uow.WithinTransaction(ctx, func(ctx context.Context, repos app.Repositories) error {
		return repos.Wallets().Insert(ctx, w)
	})
	if err != nil {
		t.Fatalf("commit insert: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM wallets WHERE id = $1`, walletID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("wallet should exist after commit, count=%d", count)
	}
}
