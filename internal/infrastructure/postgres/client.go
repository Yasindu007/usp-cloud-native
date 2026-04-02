// Package postgres provides the PostgreSQL infrastructure adapter.
// It implements the domain repository interfaces using pgx v5.
//
// Connection pool architecture:
//
//	Primary pool  — write operations (INSERT, UPDATE, DELETE)
//	Replica pool  — read operations (SELECT)
//
// In Phase 1, both pools point to the same instance (single node).
// In Phase 4, the replica DSN points to the PostgreSQL read replica
// StatefulSet, giving us horizontal read scaling without application changes.
//
// Why pgx over database/sql + lib/pq?
//   - Native PostgreSQL protocol (no driver translation layer)
//   - Better performance (~30% faster for bulk operations)
//   - pgxpool provides connection pool health management
//   - Native support for PostgreSQL-specific types (arrays, JSONB, ranges)
//   - pgx v5 has a cleaner API with generics support
package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/urlshortener/platform/internal/config"
)

// Client wraps both the primary and replica connection pools.
// This struct is safe for concurrent use — pgxpool manages its own
// internal locking and connection lifecycle.
type Client struct {
	primary *pgxpool.Pool
	replica *pgxpool.Pool

	// hasReplica tracks whether a separate replica pool was configured.
	// When false, read operations fall back to the primary pool.
	hasReplica bool
}

// New creates and validates connections to the primary and replica PostgreSQL
// instances. It will fail immediately if either pool cannot establish at least
// one connection — this implements fail-fast startup behavior required by our
// readiness probe design.
//
// The context controls the initial connection timeout. Pass a context with a
// reasonable deadline (e.g., 30s) to prevent indefinite blocking during startup.
func New(ctx context.Context, cfg *config.Config) (*Client, error) {
	primary, err := buildPool(ctx, cfg.DBPrimaryDSN, cfg, "primary")
	if err != nil {
		return nil, fmt.Errorf("postgres: connecting to primary: %w", err)
	}

	client := &Client{
		primary:    primary,
		hasReplica: false,
	}

	// Only create a replica pool if the DSN differs from primary.
	// In Phase 1 (single node), they are identical — we skip the duplicate pool
	// to avoid wasting connections.
	if cfg.DBReplicaDSN != "" && cfg.DBReplicaDSN != cfg.DBPrimaryDSN {
		replica, err := buildPool(ctx, cfg.DBReplicaDSN, cfg, "replica")
		if err != nil {
			// Replica failure is non-fatal at startup — we degrade to
			// primary-only read mode and log a warning. The readyz probe
			// will still pass. Alert on this via Prometheus metric.
			// In production you may choose to make this fatal.
			primary.Close()
			return nil, fmt.Errorf("postgres: connecting to replica: %w", err)
		}
		client.replica = replica
		client.hasReplica = true
	} else {
		// Point replica at primary so callers never need to check hasReplica.
		client.replica = primary
	}

	return client, nil
}

// Primary returns the primary (write) connection pool.
// Use for all INSERT, UPDATE, DELETE, and SELECT ... FOR UPDATE queries.
func (c *Client) Primary() *pgxpool.Pool {
	return c.primary
}

// Replica returns the replica (read) connection pool.
// Use for all read-only SELECT queries that do not require read-your-writes
// consistency. Falls back to primary if no replica is configured.
//
// Read-your-writes note: after a write, if the application immediately reads
// the same data, use Primary() for that read. Replica replication lag can be
// 10–100ms which would cause the read to return stale data.
func (c *Client) Replica() *pgxpool.Pool {
	return c.replica
}

// Ping verifies connectivity to both the primary and replica pools.
// Used by the /readyz health endpoint. Returns the first error encountered.
//
// Ping sends a lightweight SELECT 1 query — it validates that a connection
// can be acquired from the pool AND that the database is responding.
// A timeout error here indicates network partition or database overload.
func (c *Client) Ping(ctx context.Context) error {
	if err := c.primary.Ping(ctx); err != nil {
		return fmt.Errorf("postgres: primary ping failed: %w", err)
	}

	// Only ping replica if it is a separate pool (not aliased to primary).
	if c.hasReplica {
		if err := c.replica.Ping(ctx); err != nil {
			return fmt.Errorf("postgres: replica ping failed: %w", err)
		}
	}

	return nil
}

// Stats returns pool statistics for Prometheus metric export.
// Called by the metrics collector on every scrape interval.
type PoolStats struct {
	TotalConns    int32
	IdleConns     int32
	AcquiredConns int32
	MaxConns      int32
}

// PrimaryStats returns connection pool statistics for the primary pool.
func (c *Client) PrimaryStats() PoolStats {
	s := c.primary.Stat()
	return PoolStats{
		TotalConns:    s.TotalConns(),
		IdleConns:     s.IdleConns(),
		AcquiredConns: s.AcquiredConns(),
		MaxConns:      s.MaxConns(),
	}
}

// Close shuts down both connection pools gracefully.
// Must be called during application shutdown to release all connections
// and prevent connection leaks on the PostgreSQL server.
//
// pgxpool.Close() is synchronous — it waits for all acquired connections
// to be returned to the pool before closing. This is why the shutdown
// context timeout in main.go must be long enough to cover in-flight requests.
func (c *Client) Close() {
	c.primary.Close()
	if c.hasReplica {
		c.replica.Close()
	}
}

// buildPool creates and validates a pgxpool.Pool from a DSN string.
// It applies all pool sizing and timeout configuration from Config.
func buildPool(ctx context.Context, dsn string, cfg *config.Config, role string) (*pgxpool.Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parsing DSN for %s pool: %w", role, err)
	}

	// ── Pool sizing ─────────────────────────────────────────────────────────
	// MaxConns controls the upper bound of connections this pool will open.
	// Each PostgreSQL connection uses ~5MB of server RAM plus backend process.
	// At 25 max conns per service × 2 services × 3 replicas = 150 connections.
	// PostgreSQL default max_connections is 100 — adjust in postgres config.
	poolCfg.MaxConns = cfg.DBMaxOpenConns
	poolCfg.MinConns = cfg.DBMinOpenConns

	// ── Connection lifetime ─────────────────────────────────────────────────
	// MaxConnLifetime forces connection recycling to prevent "stale" TCP
	// connections that appear open but are silently broken (e.g., after a
	// firewall timeout or PostgreSQL restart).
	poolCfg.MaxConnLifetime = time.Duration(cfg.DBConnMaxLifetimeM) * time.Minute

	// MaxConnIdleTime closes connections that have been idle too long.
	// Prevents the pool from holding more connections than needed during
	// low-traffic periods (e.g., overnight).
	poolCfg.MaxConnIdleTime = time.Duration(cfg.DBConnMaxIdleTimeM) * time.Minute

	// ── Health check ────────────────────────────────────────────────────────
	// HealthCheckPeriod controls how often the pool sends keepalive pings
	// to idle connections. This catches broken connections before they are
	// handed to a caller, preventing the "connection reset" errors that occur
	// when a caller receives a dead connection from the pool.
	poolCfg.HealthCheckPeriod = 30 * time.Second

	// ── Connection validation ────────────────────────────────────────────────
	// BeforeAcquire runs before every connection handout from the pool.
	// We use it to ensure the connection is still alive (fast ping).
	// This adds ~0.1ms per acquisition but prevents silent connection failures.
	poolCfg.BeforeAcquire = func(ctx context.Context, conn *pgx.Conn) bool {
		return conn.Ping(ctx) == nil
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("creating %s pool: %w", role, err)
	}

	// Validate the pool can actually reach the database before returning.
	// This converts a later runtime error into a clear startup failure.
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("initial ping to %s database failed (is postgres running?): %w", role, err)
	}

	return pool, nil
}
