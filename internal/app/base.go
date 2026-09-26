// Package app provides the shared bootstrap used by every binary (api, worker,
// seed): configuration, logging, the database pool, Redis, tracing, and metrics.
package app

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"log/slog"

	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/config"
	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/observability"
	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/platform"
	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/store"
)

// Base holds the infrastructure dependencies shared by all binaries.
type Base struct {
	Cfg            config.Config
	Logger         *slog.Logger
	Pool           *pgxpool.Pool
	Redis          *redis.Client
	Metrics        *observability.Metrics
	tracerShutdown observability.ShutdownFunc
}

// NewBase loads config, initialises tracing, runs migrations, and connects to
// PostgreSQL and Redis. Call Close to release everything on shutdown.
func NewBase(ctx context.Context, runMigrations bool) (*Base, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	logger := platform.NewLogger(cfg.Observability.LogLevel)

	shutdown, err := observability.InitTracing(ctx, cfg.Observability.ServiceName, cfg.Observability.OTLPEndpoint, cfg.Observability.OTelEnabled)
	if err != nil {
		// Tracing is best-effort; log and continue without it.
		logger.Warn("tracing disabled: init failed", "error", err)
		shutdown = func(context.Context) error { return nil }
	}

	if runMigrations {
		if err := store.Migrate(cfg.DB.URL()); err != nil {
			return nil, fmt.Errorf("run migrations: %w", err)
		}
		logger.Info("database migrations applied")
	}

	pool, err := platform.NewPostgresPool(ctx, cfg.DB)
	if err != nil {
		return nil, fmt.Errorf("connect postgres: %w", err)
	}

	rdb, err := platform.NewRedisClient(ctx, cfg.Redis)
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect redis: %w", err)
	}

	return &Base{
		Cfg:            cfg,
		Logger:         logger,
		Pool:           pool,
		Redis:          rdb,
		Metrics:        observability.NewMetrics(),
		tracerShutdown: shutdown,
	}, nil
}

// Close releases all infrastructure resources.
func (b *Base) Close(ctx context.Context) {
	if b.tracerShutdown != nil {
		_ = b.tracerShutdown(ctx)
	}
	if b.Redis != nil {
		_ = b.Redis.Close()
	}
	if b.Pool != nil {
		b.Pool.Close()
	}
}
