package observability

import (
	"log/slog"
	"os"

	"go.uber.org/fx"
)

var Module = fx.Module("observability",
	fx.Provide(NewLogger),
	fx.Provide(NewMetrics),
	fx.Provide(NewHealthHandler),
)

func NewLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
}
