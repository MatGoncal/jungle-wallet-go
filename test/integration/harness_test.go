//go:build integration

package integration

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
)

var (
	sharedDBURL       string
	sharedSQSEndpoint string
	sharedWagerURL    string
	sharedEventsURL   string
	sharedDLQURL      string
	sharedOIDCIssuer  string
	sharedOIDCDisc    string
	sharedPool        *pgxpool.Pool
	migrationsDir     string
	repoRoot          string
)

func TestMain(m *testing.M) {
	ctx := context.Background()
	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot = filepath.Clean(filepath.Join(filepath.Dir(thisFile), "../.."))
	migrationsDir = filepath.Join(repoRoot, "migrations")

	if os.Getenv("SKIP_TESTCONTAINERS") == "1" || os.Getenv("USE_COMPOSE") == "1" {
		sharedDBURL = getenv("DATABASE_URL", "postgres://wallet:wallet@localhost:55432/wallet?sslmode=disable")
		sharedSQSEndpoint = getenv("SQS_ENDPOINT", "http://localhost:4566")
		sharedWagerURL = getenv("SQS_WAGER_QUEUE_URL", sharedSQSEndpoint+"/000000000000/wager-transactions.fifo")
		sharedEventsURL = getenv("SQS_EVENTS_QUEUE_URL", sharedSQSEndpoint+"/000000000000/wallet-events.fifo")
		sharedDLQURL = getenv("SQS_WAGER_DLQ_URL", sharedSQSEndpoint+"/000000000000/wager-transactions-dlq.fifo")
		sharedOIDCIssuer = getenv("OIDC_ISSUER_URL", "http://localhost:8081/realms/jungle-wallet")
		sharedOIDCDisc = getenv("OIDC_DISCOVERY_URL", sharedOIDCIssuer)
		pool, err := pgxpool.New(context.Background(), sharedDBURL)
		if err != nil {
			fmt.Fprintf(os.Stderr, "pool: %v\n", err)
			os.Exit(1)
		}
		sharedPool = pool
		defer pool.Close()
		os.Exit(m.Run())
	}

	netw, err := network.New(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "network: %v — falling back to compose\n", err)
		os.Exit(runWithCompose(m))
	}

	pg, err := postgres.Run(ctx,
		"postgres:16-alpine",
		postgres.WithDatabase("wallet"),
		postgres.WithUsername("wallet"),
		postgres.WithPassword("wallet"),
		network.WithNetwork([]string{"pg"}, netw),
		testcontainers.WithWaitStrategy(wait.ForListeningPort("5432/tcp").WithStartupTimeout(60*time.Second)),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "postgres container: %v — falling back to compose\n", err)
		_ = netw.Remove(ctx)
		os.Exit(runWithCompose(m))
	}
	defer func() { _ = pg.Terminate(ctx) }()
	defer func() { _ = netw.Remove(ctx) }()

	dbURL, err := pg.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		fmt.Fprintf(os.Stderr, "postgres url: %v\n", err)
		os.Exit(1)
	}
	sharedDBURL = dbURL
	if err := applyMigrations(dbURL); err != nil {
		fmt.Fprintf(os.Stderr, "migrate up: %v\n", err)
		os.Exit(1)
	}

	initScript := filepath.Join(repoRoot, "deploy/localstack/init-sqs.sh")
	ls, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "localstack/localstack:4.3",
			ExposedPorts: []string{"4566/tcp"},
			Env: map[string]string{
				"SERVICES":              "sqs,iam",
				"DEBUG":                 "0",
				"AWS_DEFAULT_REGION":    "us-east-1",
				"AWS_ACCESS_KEY_ID":     "test",
				"AWS_SECRET_ACCESS_KEY": "test",
			},
			Files: []testcontainers.ContainerFile{{
				HostFilePath:      initScript,
				ContainerFilePath: "/etc/localstack/init/ready.d/init-sqs.sh",
				FileMode:          0o755,
			}},
			Networks: []string{netw.Name},
			NetworkAliases: map[string][]string{
				netw.Name: {"localstack"},
			},
			WaitingFor: wait.ForHTTP("/_localstack/health").WithPort("4566/tcp").WithStartupTimeout(90 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "localstack: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = ls.Terminate(ctx) }()

	lsHost, err := ls.Host(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "localstack host: %v\n", err)
		os.Exit(1)
	}
	lsPort, err := ls.MappedPort(ctx, "4566/tcp")
	if err != nil {
		fmt.Fprintf(os.Stderr, "localstack port: %v\n", err)
		os.Exit(1)
	}
	sharedSQSEndpoint = fmt.Sprintf("http://%s", net.JoinHostPort(lsHost, lsPort.Port()))
	sharedWagerURL = sharedSQSEndpoint + "/000000000000/wager-transactions.fifo"
	sharedEventsURL = sharedSQSEndpoint + "/000000000000/wallet-events.fifo"
	sharedDLQURL = sharedSQSEndpoint + "/000000000000/wager-transactions-dlq.fifo"
	if err := ensureQueues(ctx, sharedSQSEndpoint); err != nil {
		fmt.Fprintf(os.Stderr, "ensure queues: %v\n", err)
		os.Exit(1)
	}

	realmPath := filepath.Join(repoRoot, "deploy/keycloak/jungle-wallet-realm.json")
	kc, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "quay.io/keycloak/keycloak:26.1.2",
			ExposedPorts: []string{"8080/tcp"},
			Cmd:          []string{"start-dev", "--import-realm"},
			Env: map[string]string{
				"KEYCLOAK_ADMIN":          "admin",
				"KEYCLOAK_ADMIN_PASSWORD": "admin",
				"KC_HEALTH_ENABLED":       "true",
			},
			Files: []testcontainers.ContainerFile{{
				HostFilePath:      realmPath,
				ContainerFilePath: "/opt/keycloak/data/import/jungle-wallet-realm.json",
				FileMode:          0o644,
			}},
			Networks: []string{netw.Name},
			NetworkAliases: map[string][]string{
				netw.Name: {"keycloak"},
			},
			WaitingFor: wait.ForHTTP("/realms/jungle-wallet").WithPort("8080/tcp").WithStartupTimeout(180 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "keycloak: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = kc.Terminate(ctx) }()

	kcHost, err := kc.Host(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "keycloak host: %v\n", err)
		os.Exit(1)
	}
	kcPort, err := kc.MappedPort(ctx, "8080/tcp")
	if err != nil {
		fmt.Fprintf(os.Stderr, "keycloak port: %v\n", err)
		os.Exit(1)
	}
	sharedOIDCIssuer = fmt.Sprintf("http://%s/realms/jungle-wallet", net.JoinHostPort(kcHost, kcPort.Port()))
	sharedOIDCDisc = sharedOIDCIssuer

	pool, err := pgxpool.New(ctx, sharedDBURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pool: %v\n", err)
		os.Exit(1)
	}
	sharedPool = pool
	defer pool.Close()

	_ = os.Setenv("DATABASE_URL", sharedDBURL)
	_ = os.Setenv("SQS_ENDPOINT", sharedSQSEndpoint)
	_ = os.Setenv("SQS_WAGER_QUEUE_URL", sharedWagerURL)
	_ = os.Setenv("SQS_EVENTS_QUEUE_URL", sharedEventsURL)
	_ = os.Setenv("OIDC_ISSUER_URL", sharedOIDCIssuer)
	_ = os.Setenv("OIDC_DISCOVERY_URL", sharedOIDCDisc)
	_ = os.Setenv("AWS_ACCESS_KEY_ID", "test")
	_ = os.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	_ = os.Setenv("AWS_REGION", "us-east-1")

	os.Exit(m.Run())
}

func runWithCompose(m *testing.M) int {
	compose := filepath.Join(repoRoot, "deploy/docker-compose.yml")
	up := exec.Command("docker", "compose", "-f", compose, "up", "-d", "postgres", "localstack", "keycloak", "migrate")
	up.Stdout, up.Stderr = os.Stdout, os.Stderr
	if err := up.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "compose up: %v\n", err)
		return 1
	}
	sharedDBURL = "postgres://wallet:wallet@localhost:55432/wallet?sslmode=disable"
	sharedSQSEndpoint = "http://localhost:4566"
	sharedWagerURL = sharedSQSEndpoint + "/000000000000/wager-transactions.fifo"
	sharedEventsURL = sharedSQSEndpoint + "/000000000000/wallet-events.fifo"
	sharedDLQURL = sharedSQSEndpoint + "/000000000000/wager-transactions-dlq.fifo"
	sharedOIDCIssuer = "http://localhost:8081/realms/jungle-wallet"
	sharedOIDCDisc = sharedOIDCIssuer
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		pool, err := pgxpool.New(context.Background(), sharedDBURL)
		if err == nil {
			if err := pool.Ping(context.Background()); err == nil {
				sharedPool = pool
				break
			}
			pool.Close()
		}
		time.Sleep(2 * time.Second)
	}
	if sharedPool == nil {
		fmt.Fprintln(os.Stderr, "postgres not ready via compose")
		return 1
	}
	defer sharedPool.Close()
	for time.Now().Before(deadline) {
		resp, err := http.Get(sharedOIDCIssuer)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == 200 {
				break
			}
		}
		time.Sleep(2 * time.Second)
	}
	_ = ensureQueues(context.Background(), sharedSQSEndpoint)
	_ = os.Setenv("DATABASE_URL", sharedDBURL)
	_ = os.Setenv("SQS_ENDPOINT", sharedSQSEndpoint)
	_ = os.Setenv("OIDC_ISSUER_URL", sharedOIDCIssuer)
	_ = os.Setenv("OIDC_DISCOVERY_URL", sharedOIDCDisc)
	return m.Run()
}

func getenv(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func applyMigrations(dbURL string) error {
	sqlBytes, err := os.ReadFile(filepath.Join(migrationsDir, "000001_init.up.sql"))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	_, err = pool.Exec(ctx, string(sqlBytes))
	return err
}

func migrateDown(dbURL string) error {
	sqlBytes, err := os.ReadFile(filepath.Join(migrationsDir, "000001_init.down.sql"))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	_, err = pool.Exec(ctx, string(sqlBytes))
	return err
}

func migrateUp(dbURL string) error { return applyMigrations(dbURL) }

func ensureQueues(ctx context.Context, endpoint string) error {
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion("us-east-1"),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")),
	)
	if err != nil {
		return err
	}
	client := awssqs.NewFromConfig(awsCfg, func(o *awssqs.Options) {
		o.BaseEndpoint = aws.String(endpoint)
	})
	create := func(name string, attrs map[string]string) (string, error) {
		out, err := client.CreateQueue(ctx, &awssqs.CreateQueueInput{
			QueueName:  aws.String(name),
			Attributes: attrs,
		})
		if err != nil {
			gu, err2 := client.GetQueueUrl(ctx, &awssqs.GetQueueUrlInput{QueueName: aws.String(name)})
			if err2 != nil {
				return "", err
			}
			return aws.ToString(gu.QueueUrl), nil
		}
		return aws.ToString(out.QueueUrl), nil
	}
	dlqURL, err := create("wager-transactions-dlq.fifo", map[string]string{
		"FifoQueue": "true", "ContentBasedDeduplication": "false",
	})
	if err != nil {
		return err
	}
	attrs, err := client.GetQueueAttributes(ctx, &awssqs.GetQueueAttributesInput{
		QueueUrl: aws.String(dlqURL), AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn},
	})
	if err != nil {
		return err
	}
	dlqARN := attrs.Attributes[string(types.QueueAttributeNameQueueArn)]
	_, err = create("wager-transactions.fifo", map[string]string{
		"FifoQueue": "true", "ContentBasedDeduplication": "false", "VisibilityTimeout": "30",
		"RedrivePolicy": fmt.Sprintf(`{"deadLetterTargetArn":"%s","maxReceiveCount":"5"}`, dlqARN),
	})
	if err != nil {
		return err
	}
	_, err = create("wallet-events.fifo", map[string]string{
		"FifoQueue": "true", "ContentBasedDeduplication": "false", "VisibilityTimeout": "30",
	})
	return err
}

func openPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if sharedPool != nil {
		return sharedPool
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, sharedDBURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	sharedPool = pool
	return pool
}

func mustV7(t *testing.T) uuid.UUID {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.New()
	}
	return id
}

func insertWallet(t *testing.T, pool *pgxpool.Pool, id, player uuid.UUID, balance int64) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO wallets (id, player_id, currency, balance_minor, version, created_at, updated_at)
		VALUES ($1,$2,'BRL',$3,1,NOW(),NOW())`, id, player, balance)
	if err != nil {
		t.Fatalf("insert wallet: %v", err)
	}
}

func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

func waitReady(t *testing.T, base string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(base + "/health/live")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == 200 {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("api not ready at %s", base)
}
