package observability_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/matheusgoncalves/jungle-wallet-go/internal/infra/observability"
)

func TestLoggerFromContext_IncludesTracingFields(t *testing.T) {
	ctx := context.Background()
	ctx = observability.WithCorrelationID(ctx, "c-1")
	ctx = observability.WithMessageID(ctx, "m-1")
	ctx = observability.WithTransactionID(ctx, "t-1")
	ctx = observability.WithWalletID(ctx, "w-1")
	ctx = observability.WithProviderID(ctx, "provider-a")

	if observability.CorrelationID(ctx) != "c-1" {
		t.Fatal("correlation")
	}
	log := observability.LoggerFromContext(slog.Default(), ctx)
	if log == nil {
		t.Fatal("logger")
	}
	_ = observability.NewMetrics()
}
