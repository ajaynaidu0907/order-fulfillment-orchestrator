package cache

import (
	"context"
	"fmt"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
)

// startRedis starts a Redis container, converting the panic that testcontainers
// raises when no Docker host can be found into an ordinary error, so callers can
// skip cleanly.
func startRedis(ctx context.Context) (c *tcredis.RedisContainer, err error) {
	defer func() {
		if r := recover(); r != nil {
			c, err = nil, fmt.Errorf("docker unavailable: %v", r)
		}
	}()
	return tcredis.Run(ctx, "redis:7-alpine")
}

// newRedisClient starts a throwaway Redis container and returns a connected
// client. It skips the test cleanly when Docker is not available, so the suite
// stays green without a Docker daemon.
func newRedisClient(t *testing.T) *goredis.Client {
	t.Helper()
	ctx := context.Background()

	container, err := startRedis(ctx)
	if err != nil {
		t.Skipf("docker not available: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })

	connStr, err := container.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}
	opt, err := goredis.ParseURL(connStr)
	if err != nil {
		t.Fatalf("parse redis url: %v", err)
	}
	client := goredis.NewClient(opt)
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestCache_SetGetDelete(t *testing.T) {
	c := NewCache(newRedisClient(t))
	ctx := context.Background()

	// A missing key reports found=false with a nil error.
	if _, found, err := c.Get(ctx, "missing"); err != nil || found {
		t.Fatalf("expected miss, got found=%v err=%v", found, err)
	}

	if err := c.Set(ctx, "k", []byte("v"), time.Minute); err != nil {
		t.Fatalf("set: %v", err)
	}
	b, found, err := c.Get(ctx, "k")
	if err != nil || !found || string(b) != "v" {
		t.Fatalf("get: b=%q found=%v err=%v", b, found, err)
	}

	if err := c.Delete(ctx, "k"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, found, _ := c.Get(ctx, "k"); found {
		t.Fatalf("expected key to be deleted")
	}
}

func TestLocker_AcquireReleaseContention(t *testing.T) {
	l := NewLocker(newRedisClient(t))
	ctx := context.Background()

	release, ok, err := l.Acquire(ctx, "lock:x", 10*time.Second)
	if err != nil || !ok {
		t.Fatalf("first acquire: ok=%v err=%v", ok, err)
	}

	// Contention: a second acquire while the lock is held must fail.
	if _, ok2, err := l.Acquire(ctx, "lock:x", 10*time.Second); err != nil || ok2 {
		t.Fatalf("expected contention, got ok=%v err=%v", ok2, err)
	}

	// Releasing frees the lock so it can be taken again.
	if err := release(ctx); err != nil {
		t.Fatalf("release: %v", err)
	}
	release2, ok, err := l.Acquire(ctx, "lock:x", 10*time.Second)
	if err != nil || !ok {
		t.Fatalf("re-acquire after release: ok=%v err=%v", ok, err)
	}
	_ = release2(ctx)
}

func TestLocker_ReleaseIsCompareAndDelete(t *testing.T) {
	l := NewLocker(newRedisClient(t))
	ctx := context.Background()

	release1, ok, err := l.Acquire(ctx, "lock:cas", 500*time.Millisecond)
	if err != nil || !ok {
		t.Fatalf("acquire1: ok=%v err=%v", ok, err)
	}

	// Let the lock expire, then a new holder takes it.
	time.Sleep(800 * time.Millisecond)
	release2, ok, err := l.Acquire(ctx, "lock:cas", 5*time.Second)
	if err != nil || !ok {
		t.Fatalf("acquire2 after expiry: ok=%v err=%v", ok, err)
	}

	// release1's token no longer matches, so it must NOT free release2's lock.
	if err := release1(ctx); err != nil {
		t.Fatalf("release1: %v", err)
	}
	if _, ok, _ := l.Acquire(ctx, "lock:cas", time.Second); ok {
		t.Fatalf("stale release wrongly freed the current holder's lock")
	}
	_ = release2(ctx)
}

func TestRateLimiter_AllowDeny(t *testing.T) {
	rl := NewRateLimiter(newRedisClient(t))
	ctx := context.Background()

	const limit = 3
	for i := 1; i <= limit; i++ {
		allowed, err := rl.Allow(ctx, "client-1", limit, time.Minute)
		if err != nil || !allowed {
			t.Fatalf("request %d within limit should be allowed: allowed=%v err=%v", i, allowed, err)
		}
	}
	// The next request exceeds the limit.
	if allowed, err := rl.Allow(ctx, "client-1", limit, time.Minute); err != nil || allowed {
		t.Fatalf("request over limit should be denied: allowed=%v err=%v", allowed, err)
	}
	// A different key has its own independent budget.
	if allowed, err := rl.Allow(ctx, "client-2", limit, time.Minute); err != nil || !allowed {
		t.Fatalf("independent key should be allowed: allowed=%v err=%v", allowed, err)
	}
}
