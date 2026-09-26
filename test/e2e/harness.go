// Package e2e provides a reusable Testcontainers harness and store-level
// integration tests that validate the persistence foundation (OrderStore and
// JobQueue) end to end against a real PostgreSQL instance.
//
// Every test that needs a container calls StartPostgres or StartRedis, which spin
// up a throwaway container via Testcontainers. When Docker is not available the
// helpers call t.Skip, so `go test ./...` stays green without Docker. The helpers
// are exported and self-contained so higher-level end-to-end tests can reuse them.
package e2e

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"

	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/store"
)

// runPostgresContainer starts a PostgreSQL container, converting the panic that
// some testcontainers versions raise when no Docker host is reachable into an
// ordinary error so callers can t.Skip instead of crashing the test binary.
func runPostgresContainer(ctx context.Context) (c *postgres.PostgresContainer, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("docker not available: %v", r)
		}
	}()
	return postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("ofo_test"),
		postgres.WithUsername("ofo"),
		postgres.WithPassword("ofo_test_password"),
		postgres.BasicWaitStrategies(),
	)
}

// runRedisContainer starts a Redis container, converting a no-Docker panic into
// an error (see runPostgresContainer).
func runRedisContainer(ctx context.Context) (c *tcredis.RedisContainer, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("docker not available: %v", r)
		}
	}()
	return tcredis.Run(ctx, "redis:7-alpine")
}

// StartPostgres launches a throwaway PostgreSQL container, runs all schema
// migrations against it, and returns a ready connection pool. If the container
// cannot start (typically because Docker is unavailable) the current test is
// skipped rather than failed. The returned cleanup closes the pool and terminates
// the container, and callers should defer it.
func StartPostgres(t *testing.T) (dsnURL string, pool *pgxpool.Pool, cleanup func()) {
	t.Helper()
	ctx := context.Background()

	container, err := runPostgresContainer(ctx)
	if err != nil {
		if container != nil {
			_ = container.Terminate(ctx)
		}
		t.Skipf("skipping: could not start PostgreSQL container (is Docker running?): %v", err)
	}

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		_ = container.Terminate(ctx)
		t.Fatalf("postgres connection string: %v", err)
	}

	if err := store.Migrate(dsn); err != nil {
		_ = container.Terminate(ctx)
		t.Fatalf("run migrations: %v", err)
	}

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		_ = container.Terminate(ctx)
		t.Fatalf("parse pool config: %v", err)
	}
	// Headroom for the concurrency test's parallel claimers.
	cfg.MaxConns = 20

	pool, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		_ = container.Terminate(ctx)
		t.Fatalf("open pgx pool: %v", err)
	}

	cleanup = func() {
		pool.Close()
		_ = container.Terminate(context.Background())
	}
	return dsn, pool, cleanup
}

// StartRedis launches a throwaway Redis container and returns its address and a
// connected client. If the container cannot start (typically because Docker is
// unavailable) the current test is skipped rather than failed. The returned
// cleanup closes the client and terminates the container.
func StartRedis(t *testing.T) (addr string, client *redis.Client, cleanup func()) {
	t.Helper()
	ctx := context.Background()

	container, err := runRedisContainer(ctx)
	if err != nil {
		if container != nil {
			_ = container.Terminate(ctx)
		}
		t.Skipf("skipping: could not start Redis container (is Docker running?): %v", err)
	}

	endpoint, err := container.Endpoint(ctx, "")
	if err != nil {
		_ = container.Terminate(ctx)
		t.Fatalf("redis endpoint: %v", err)
	}

	client = redis.NewClient(&redis.Options{Addr: endpoint})
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		_ = container.Terminate(ctx)
		t.Fatalf("redis ping: %v", err)
	}

	cleanup = func() {
		_ = client.Close()
		_ = container.Terminate(context.Background())
	}
	return endpoint, client, cleanup
}
