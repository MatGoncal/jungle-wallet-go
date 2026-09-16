package appfx

import (
	"context"
	"log/slog"

	"github.com/matheusgoncalves/jungle-wallet-go/internal/infra/auth"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/infra/config"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/infra/observability"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/infra/postgres"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/infra/sqs"
	httpapi "github.com/matheusgoncalves/jungle-wallet-go/internal/transport/http"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/transport/worker"
	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"
)

// Options returns the full application Fx graph (HTTP + workers + infra).
func Options() fx.Option {
	return fx.Options(
		fx.WithLogger(func(log *slog.Logger) fxevent.Logger {
			return &fxevent.SlogLogger{Logger: log}
		}),
		fx.Provide(config.Load),
		fx.Provide(func(pg *postgres.PoolChecker, sq *sqs.ReadyChecker) []observability.Checker {
			return []observability.Checker{pg, sq}
		}),
		observability.Module,
		postgres.Module,
		sqs.Module,
		auth.Module,
		httpapi.Module,
		worker.Module,
		fx.Invoke(func(log *slog.Logger, lc fx.Lifecycle) {
			lc.Append(fx.Hook{
				OnStart: func(context.Context) error {
					log.Info("jungle-wallet-go starting")
					return nil
				},
			})
		}),
	)
}
