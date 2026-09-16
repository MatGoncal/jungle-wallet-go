package worker

import (
	"context"
	"log/slog"

	"github.com/matheusgoncalves/jungle-wallet-go/internal/app"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/infra/config"
	"go.uber.org/fx"
)

var Module = fx.Module("worker",
	fx.Provide(app.NewResolvePendingReference),
	fx.Provide(NewConsumer),
	fx.Provide(NewOutboxPublisher),
	fx.Provide(NewReferenceWorker),
	fx.Invoke(RegisterLifecycle),
)

func RegisterLifecycle(
	lc fx.Lifecycle,
	cfg config.Config,
	log *slog.Logger,
	consumer *Consumer,
	outbox *OutboxPublisher,
	refs *ReferenceWorker,
) {
	var root context.Context
	var cancel context.CancelFunc
	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			root, cancel = context.WithCancel(context.Background())
			consumer.Start(root)
			outbox.Start(root)
			refs.Start(root)
			log.Info("workers started", "wagerQueue", cfg.WagerQueueURL, "eventsQueue", cfg.EventsQueueURL)
			return nil
		},
		OnStop: func(ctx context.Context) error {
			log.Info("workers shutting down on SIGTERM/stop")
			if cancel != nil {
				cancel()
			}
			// Consumer stops polling first and drains/releases in-flight messages.
			_ = consumer.Stop(ctx)
			_ = outbox.Stop(ctx)
			_ = refs.Stop(ctx)
			log.Info("workers stopped")
			return nil
		},
	})
}
