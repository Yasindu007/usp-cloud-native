// Package redis provides the Redis infrastructure adapter.
// It implements domain/url.CachePort using go-redis v9.
//
// Redis role in this system:
//
//	The redirect hot path (GET /{shortcode}) is the most latency-sensitive
//	operation in the platform. Our SLO is P99 < 50ms. A PostgreSQL lookup
//	with connection pool acquisition, query execution, and network RTT costs
//	~5–15ms under normal load. Redis, being in-process memory with a simple
//	GET command, costs ~0.3–1ms. For high-traffic short codes (e.g., a viral
//	tweet), the cache prevents thousands of identical DB queries per second.
//
// Cache topology (Phase 1):
//
//	Single Redis node. In Phase 4 we move to Redis Cluster (3 nodes) for
//	HA. The CachePort interface abstracts this — the application layer
//	never knows whether it's talking to a single node or a cluster.
//
// Why go-redis v9 over redigo?
//   - Native context support (propagates cancellation and deadlines)
//   - Type-safe command API (no interface{} casts)
//   - Built-in connection pool with health checks
//   - First-class support for Redis Cluster and Sentinel
//   - OpenTelemetry hook support (added in Phase 4)
package redis

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/urlshortener/platform/internal/config"
)

// Client wraps the go-redis client and exposes only what the
// application layer needs. This prevents go-redis types from leaking
// into the application and domain layers.
type Client struct {
	rdb *redis.Client
}

// New creates a configured Redis client and validates the connection.
// Fails fast if Redis is unreachable — same philosophy as the Postgres client.
//
// The context passed here controls the initial connection validation timeout.
// Pass a context with a reasonable deadline (e.g., 10s).
func New(ctx context.Context, cfg *config.Config) (*Client, error) {
	opts := &redis.Options{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPassword,
		DB:       cfg.RedisDB,

		// ── Pool configuration ──────────────────────────────────────────────
		// PoolSize is the maximum number of socket connections per CPU.
		// For a single-node client this is the absolute max connections.
		PoolSize: cfg.RedisPoolSize,

		// MinIdleConns keeps this many connections warm even during idle periods.
		// Prevents cold-start latency spikes when traffic resumes after quiet periods.
		MinIdleConns: cfg.RedisMinIdleConns,

		// ── Timeouts ────────────────────────────────────────────────────────
		// DialTimeout: time allowed to establish a new connection.
		DialTimeout: time.Duration(cfg.RedisDialTimeoutS) * time.Second,

		// ReadTimeout: time allowed for a single command read.
		// If Redis doesn't respond within this window, the command fails.
		// 3s is generous — a Redis GET on a warm key should take < 1ms.
		// We set it high to tolerate transient load spikes without cascading failures.
		ReadTimeout: time.Duration(cfg.RedisReadTimeoutS) * time.Second,

		// WriteTimeout: time allowed for a single command write.
		WriteTimeout: time.Duration(cfg.RedisWriteTimeoutS) * time.Second,

		// ── Connection health ────────────────────────────────────────────────
		// ConnMaxIdleTime closes connections idle longer than this duration.
		// Prevents stale connections accumulating in the pool.
		ConnMaxIdleTime: 5 * time.Minute,

		// ConnMaxLifetime forces periodic reconnection even for active connections.
		// Guards against silent TCP connection corruption.
		ConnMaxLifetime: 30 * time.Minute,
	}

	rdb := redis.NewClient(opts)

	// Validate connectivity before returning.
	// redis.NewClient() is lazy — it does not connect until the first command.
	// We force a PING here so startup fails immediately if Redis is down.
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	if err := rdb.Ping(pingCtx).Err(); err != nil {
		_ = rdb.Close()
		return nil, fmt.Errorf("redis: initial ping failed (is redis running?): %w", err)
	}

	return &Client{rdb: rdb}, nil
}

// RDB exposes the underlying go-redis client for use by adapters in this package.
// It is intentionally unexported to the outside world — external code uses
// the typed methods on URLCache rather than raw Redis commands.
func (c *Client) RDB() *redis.Client {
	return c.rdb
}

// Ping sends a PING command to Redis and returns an error if it fails.
// Used by the /readyz health endpoint.
func (c *Client) Ping(ctx context.Context) error {
	if err := c.rdb.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("redis: ping failed: %w", err)
	}
	return nil
}

// PoolStats returns connection pool statistics for Prometheus metric export.
type PoolStats struct {
	TotalConns uint32
	IdleConns  uint32
	StaleConns uint32
	Hits       uint32 // Pool hits (connection reused)
	Misses     uint32 // Pool misses (new connection created)
	Timeouts   uint32 // Pool timeouts (all connections busy)
}

// Stats returns current connection pool statistics.
func (c *Client) Stats() PoolStats {
	s := c.rdb.PoolStats()
	return PoolStats{
		TotalConns: s.TotalConns,
		IdleConns:  s.IdleConns,
		StaleConns: s.StaleConns,
		Hits:       s.Hits,
		Misses:     s.Misses,
		Timeouts:   s.Timeouts,
	}
}

// Close gracefully shuts down the Redis connection pool.
// Must be called during application shutdown.
// In-flight commands are allowed to complete before connections close.
func (c *Client) Close() error {
	if err := c.rdb.Close(); err != nil {
		return fmt.Errorf("redis: closing client: %w", err)
	}
	return nil
}
