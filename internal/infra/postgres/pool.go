package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/infra/config"
	"go.uber.org/fx"
)

var Module = fx.Module("postgres",
	fx.Provide(NewPool),
	fx.Provide(NewPoolChecker),
	fx.Provide(NewUnitOfWork),
)

func NewPool(lc fx.Lifecycle, cfg config.Config) (*pgxpool.Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	poolCfg.MaxConns = 20
	poolCfg.MinConns = 2
	poolCfg.MaxConnLifetime = 30 * time.Minute

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("connect postgres: %w", err)
	}

	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			return pool.Ping(pingCtx)
		},
		OnStop: func(context.Context) error {
			pool.Close()
			return nil
		},
	})

	return pool, nil
}

// PoolChecker implements readiness against PostgreSQL.
type PoolChecker struct {
	pool *pgxpool.Pool
}

func NewPoolChecker(pool *pgxpool.Pool) *PoolChecker {
	return &PoolChecker{pool: pool}
}

func (c *PoolChecker) Name() string { return "postgres" }

func (c *PoolChecker) Check(ctx context.Context) error {
	return c.pool.Ping(ctx)
}
