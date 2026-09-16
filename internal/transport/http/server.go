package httpapi

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/matheusgoncalves/jungle-wallet-go/internal/app"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/infra/auth"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/infra/config"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/infra/observability"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/infra/postgres"
	"go.uber.org/fx"
)

var Module = fx.Module("http",
	fx.Provide(func(uow *postgres.UnitOfWork) app.UnitOfWork { return uow }),
	fx.Provide(app.NewOpenWallet),
	fx.Provide(app.NewProcessWagerTransaction),
	fx.Provide(app.NewGetWallet),
	fx.Provide(app.NewGetTransaction),
	fx.Provide(app.NewListLedger),
	fx.Provide(app.NewReconcileWallet),
	fx.Provide(func(
		health *observability.HealthHandler,
		verifier *auth.Verifier,
		metrics *observability.Metrics,
		log *slog.Logger,
		openWallet *app.OpenWallet,
		process *app.ProcessWagerTransaction,
		getWallet *app.GetWallet,
		getTx *app.GetTransaction,
		listLedger *app.ListLedger,
		reconcile *app.ReconcileWallet,
	) http.Handler {
		return NewMux(Dependencies{
			Health: health, Auth: verifier, Metrics: metrics, Log: log,
			OpenWallet: openWallet, ProcessWager: process,
			GetWallet: getWallet, GetTx: getTx, ListLedger: listLedger, Reconcile: reconcile,
		})
	}),
	fx.Provide(NewServer),
	fx.Invoke(RegisterLifecycle),
)

func NewServer(cfg config.Config, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}

func RegisterLifecycle(lc fx.Lifecycle, server *http.Server, cfg config.Config, log *slog.Logger) {
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			ln, err := net.Listen("tcp", server.Addr)
			if err != nil {
				return fmt.Errorf("listen %s: %w", server.Addr, err)
			}
			log.Info("http server starting", "addr", server.Addr)
			go func() {
				if err := server.Serve(ln); err != nil && err != http.ErrServerClosed {
					log.Error("http server failed", "error", err)
				}
			}()
			return nil
		},
		OnStop: func(ctx context.Context) error {
			shutdownCtx, cancel := context.WithTimeout(ctx, cfg.ShutdownTimeout)
			defer cancel()
			log.Info("http server shutting down")
			return server.Shutdown(shutdownCtx)
		},
	})
}
