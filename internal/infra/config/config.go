package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config holds process configuration loaded from the environment.
type Config struct {
	HTTPAddr           string
	DatabaseURL        string
	SQSEndpoint        string
	SQSRegion          string
	SQSAccessKeyID     string
	SQSSecretAccessKey string
	WagerQueueURL      string
	EventsQueueURL     string
	OIDCIssuerURL      string
	OIDCDiscoveryURL   string // optional; when set, JWKS/discovery use this while iss must match OIDCIssuerURL
	OIDCAudience       string
	ShutdownTimeout    time.Duration
	ReadyCheckTimeout  time.Duration
}

func Load() (Config, error) {
	cfg := Config{
		HTTPAddr:           getenv("HTTP_ADDR", ":8080"),
		DatabaseURL:        getenv("DATABASE_URL", "postgres://wallet:wallet@localhost:55432/wallet?sslmode=disable"),
		SQSEndpoint:        getenv("SQS_ENDPOINT", "http://localhost:4566"),
		SQSRegion:          getenv("AWS_REGION", "us-east-1"),
		SQSAccessKeyID:     getenv("AWS_ACCESS_KEY_ID", "test"),
		SQSSecretAccessKey: getenv("AWS_SECRET_ACCESS_KEY", "test"),
		WagerQueueURL:      getenv("SQS_WAGER_QUEUE_URL", "http://localhost:4566/000000000000/wager-transactions.fifo"),
		EventsQueueURL:     getenv("SQS_EVENTS_QUEUE_URL", "http://localhost:4566/000000000000/wallet-events.fifo"),
		OIDCIssuerURL:      getenv("OIDC_ISSUER_URL", "http://localhost:8081/realms/jungle-wallet"),
		OIDCDiscoveryURL:   getenv("OIDC_DISCOVERY_URL", ""),
		OIDCAudience:       getenv("OIDC_AUDIENCE", ""),
		ShutdownTimeout:    durationEnv("SHUTDOWN_TIMEOUT", 15*time.Second),
		ReadyCheckTimeout:  durationEnv("READY_CHECK_TIMEOUT", 2*time.Second),
	}
	if cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
	}
	return cfg, nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func durationEnv(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	if d, err := time.ParseDuration(v); err == nil {
		return d
	}
	if sec, err := strconv.Atoi(v); err == nil {
		return time.Duration(sec) * time.Second
	}
	return fallback
}
