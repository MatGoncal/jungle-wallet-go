package observability

import (
	"context"
	"log/slog"
)

type ctxKey int

const (
	ctxCorrelationID ctxKey = iota + 1
	ctxMessageID
	ctxTransactionID
	ctxWalletID
	ctxProviderID
)

// WithCorrelationID attaches a correlation id to the context.
func WithCorrelationID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, ctxCorrelationID, id)
}

// CorrelationID returns the correlation id from context, if any.
func CorrelationID(ctx context.Context) string {
	v, _ := ctx.Value(ctxCorrelationID).(string)
	return v
}

// WithMessageID attaches an SQS/message id to the context.
func WithMessageID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, ctxMessageID, id)
}

// WithTransactionID attaches a wager transaction id.
func WithTransactionID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, ctxTransactionID, id)
}

// WithWalletID attaches a wallet id.
func WithWalletID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, ctxWalletID, id)
}

// WithProviderID attaches a provider id.
func WithProviderID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, ctxProviderID, id)
}

// LoggerFromContext returns a slog.Logger enriched with tracing fields from ctx.
// Does not log credentials or full financial payloads.
func LoggerFromContext(base *slog.Logger, ctx context.Context) *slog.Logger {
	if base == nil {
		base = slog.Default()
	}
	attrs := make([]any, 0, 10)
	if v := CorrelationID(ctx); v != "" {
		attrs = append(attrs, "correlationId", v)
	}
	if v, _ := ctx.Value(ctxMessageID).(string); v != "" {
		attrs = append(attrs, "messageId", v)
	}
	if v, _ := ctx.Value(ctxTransactionID).(string); v != "" {
		attrs = append(attrs, "transactionId", v)
	}
	if v, _ := ctx.Value(ctxWalletID).(string); v != "" {
		attrs = append(attrs, "walletId", v)
	}
	if v, _ := ctx.Value(ctxProviderID).(string); v != "" {
		attrs = append(attrs, "providerId", v)
	}
	if len(attrs) == 0 {
		return base
	}
	return base.With(attrs...)
}
