// Package cache provides the Redis-backed adapters used by the rest of the
// service: a byte-oriented read-through cache, a short-lived distributed lock,
// and a fixed-window rate limiter. Each type satisfies the matching interface
// from internal/domain, so callers depend only on those ports.
package cache

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/domain"
)

// Cache is a Redis implementation of domain.Cache.
type Cache struct {
	rdb *redis.Client
}

// NewCache builds a Cache over the given Redis client.
func NewCache(rdb *redis.Client) *Cache { return &Cache{rdb: rdb} }

var _ domain.Cache = (*Cache)(nil)

// Get returns the value stored at key. A missing key (redis.Nil) is reported as
// found=false with a nil error, not as a failure.
func (c *Cache) Get(ctx context.Context, key string) ([]byte, bool, error) {
	b, err := c.rdb.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return b, true, nil
}

// Set stores val at key with the given TTL (0 means no expiry).
func (c *Cache) Set(ctx context.Context, key string, val []byte, ttl time.Duration) error {
	return c.rdb.Set(ctx, key, val, ttl).Err()
}

// Delete removes key. Deleting a missing key is not an error.
func (c *Cache) Delete(ctx context.Context, key string) error {
	return c.rdb.Del(ctx, key).Err()
}

// Locker is a Redis implementation of domain.Locker using SET NX PX for
// acquisition and a compare-and-delete Lua script for safe release.
type Locker struct {
	rdb *redis.Client
}

// NewLocker builds a Locker over the given Redis client.
func NewLocker(rdb *redis.Client) *Locker { return &Locker{rdb: rdb} }

var _ domain.Locker = (*Locker)(nil)

// releaseScript deletes the lock key only when its stored token still matches
// the caller's token, so a holder can never release a lock that has since been
// taken over by someone else (compare-and-delete).
var releaseScript = redis.NewScript(`
if redis.call("get", KEYS[1]) == ARGV[1] then
	return redis.call("del", KEYS[1])
else
	return 0
end
`)

// Acquire attempts to take the lock for key with the given TTL. On success it
// returns a release func that frees the lock only if this caller still holds it.
// ok is false (with a nil release) when the key is already held.
func (l *Locker) Acquire(ctx context.Context, key string, ttl time.Duration) (func(context.Context) error, bool, error) {
	token := uuid.NewString()
	ok, err := l.rdb.SetNX(ctx, key, token, ttl).Result()
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	release := func(rctx context.Context) error {
		return releaseScript.Run(rctx, l.rdb, []string{key}, token).Err()
	}
	return release, true, nil
}

// RateLimiter is a Redis fixed-window counter implementing domain.RateLimiter.
type RateLimiter struct {
	rdb *redis.Client
}

// NewRateLimiter builds a RateLimiter over the given Redis client.
func NewRateLimiter(rdb *redis.Client) *RateLimiter { return &RateLimiter{rdb: rdb} }

var _ domain.RateLimiter = (*RateLimiter)(nil)

// Allow increments the counter for key and reports whether the request is within
// limit for the current fixed window. The window's expiry is set when the first
// request of the window creates the counter.
func (r *RateLimiter) Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, error) {
	rk := "rl:" + key
	n, err := r.rdb.Incr(ctx, rk).Result()
	if err != nil {
		return false, err
	}
	if n == 1 {
		if err := r.rdb.Expire(ctx, rk, window).Err(); err != nil {
			return false, err
		}
	}
	return n <= int64(limit), nil
}
