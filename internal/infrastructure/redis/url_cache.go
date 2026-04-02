package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	domainurl "github.com/urlshortener/platform/internal/domain/url"
)

const tracerName = "github.com/urlshortener/platform/internal/infrastructure/redis"

// Cache key design:
//
//	url:v1:{shortcode}          — positive cache entry (URL found)
//	url:v1:notfound:{shortcode} — negative cache entry (URL not found)
//
// Versioning (v1) in the key allows us to invalidate the entire cache
// by bumping the version — e.g., when the serialization format changes.
// Without versioning, stale cached values with old JSON shapes would be
// deserialized into the new struct, causing silent data corruption.
//
// Key length: a 7-char shortcode gives keys of ~15 chars.
// Redis stores keys in a hash table with ~50 bytes overhead per key.
// At 10M cached entries: ~500MB RAM — well within our Redis node budget.
const (
	positiveKeyPrefix = "url:v1:"
	negativeKeyPrefix = "url:v1:notfound:"
)

// cacheEntry is the serialization format for URL entities stored in Redis.
//
// Why a separate cache DTO and not serialize the domain entity directly?
//  1. The domain entity may gain fields that should not be cached
//     (e.g., internal flags, computed properties).
//  2. The cache format is a contract — changing domain struct field names
//     would silently break deserialization of existing cache entries.
//  3. Explicit JSON tags ensure the cache format is stable even if we
//     refactor domain struct field names.
//  4. We can add cache-specific fields (e.g., CachedAt for TTL debugging)
//     without polluting the domain entity.
type cacheEntry struct {
	ID          string     `json:"id"`
	WorkspaceID string     `json:"workspace_id"`
	ShortCode   string     `json:"short_code"`
	OriginalURL string     `json:"original_url"`
	Title       string     `json:"title,omitempty"`
	Status      string     `json:"status"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	CreatedBy   string     `json:"created_by"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	ClickCount  int64      `json:"click_count"`
	// CachedAt records when this entry was written.
	// Useful for debugging stale cache issues in production.
	CachedAt time.Time `json:"cached_at"`
}

// toCacheEntry converts a domain URL to its cache representation.
func toCacheEntry(u *domainurl.URL) *cacheEntry {
	return &cacheEntry{
		ID:          u.ID,
		WorkspaceID: u.WorkspaceID,
		ShortCode:   u.ShortCode,
		OriginalURL: u.OriginalURL,
		Title:       u.Title,
		Status:      string(u.Status),
		ExpiresAt:   u.ExpiresAt,
		CreatedBy:   u.CreatedBy,
		CreatedAt:   u.CreatedAt,
		UpdatedAt:   u.UpdatedAt,
		ClickCount:  u.ClickCount,
		CachedAt:    time.Now().UTC(),
	}
}

// toDomainURL converts a cache entry back to a domain URL entity.
func (e *cacheEntry) toDomainURL() *domainurl.URL {
	return &domainurl.URL{
		ID:          e.ID,
		WorkspaceID: e.WorkspaceID,
		ShortCode:   e.ShortCode,
		OriginalURL: e.OriginalURL,
		Title:       e.Title,
		Status:      domainurl.Status(e.Status),
		ExpiresAt:   e.ExpiresAt,
		CreatedBy:   e.CreatedBy,
		CreatedAt:   e.CreatedAt,
		UpdatedAt:   e.UpdatedAt,
		ClickCount:  e.ClickCount,
	}
}

// URLCache implements domain/url.CachePort.
// It provides Redis-backed caching for URL resolution, optimizing
// the redirect hot path by eliminating PostgreSQL lookups on cache hits.
type URLCache struct {
	client *Client
}

// NewURLCache creates a URLCache backed by the given Redis Client.
func NewURLCache(client *Client) *URLCache {
	return &URLCache{client: client}
}

// Get retrieves a cached URL by short code.
//
// Return semantics:
//
//	(url, nil)  — cache HIT: return the cached URL
//	(nil, nil)  — cache MISS: short code not in cache (neither positive nor negative)
//	(nil, err)  — infrastructure error: Redis unavailable or deserialization failed
//
// The redirect handler uses this return pattern to decide whether to:
//   - Serve the cached URL immediately (HIT)
//   - Fall through to PostgreSQL (MISS)
//   - Return 500 and log a critical alert (error)
//
// Negative cache entries (IsNotFound keys) are NOT returned here.
// Use IsNotFound() to check those. This separation keeps the redirect
// handler logic explicit and readable.
func (c *URLCache) Get(ctx context.Context, shortCode string) (*domainurl.URL, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "URLCache.Get",
		trace.WithAttributes(
			attribute.String("cache.operation", "GET"),
			attribute.String("url.short_code", shortCode),
		),
	)
	defer span.End()

	key := positiveKeyPrefix + shortCode

	val, err := c.client.rdb.Get(ctx, key).Bytes()
	if err != nil {
		// Cache miss — this is not an error, it's expected behavior.
		// redis.Nil is returned when the key does not exist.
		if errors.Is(err, redis.Nil) {
			span.SetAttributes(attribute.String("cache.result", "miss"))
			return nil, nil
		}
		// Any other error is an infrastructure failure.
		span.RecordError(err)
		span.SetAttributes(attribute.String("cache.result", "error"))
		return nil, fmt.Errorf("redis: GET %s: %w", key, err)
	}

	span.SetAttributes(attribute.String("cache.result", "hit"))

	var entry cacheEntry
	if err := json.Unmarshal(val, &entry); err != nil {
		// Deserialization failure means the cache entry is corrupt or from
		// an incompatible schema version. Delete it and treat as a miss.
		// This is a self-healing behavior — the next request will repopulate
		// the cache with a fresh entry.
		span.RecordError(err)
		_ = c.client.rdb.Del(ctx, key) // best-effort delete, ignore error
		return nil, fmt.Errorf("redis: deserializing cache entry for %s: %w", shortCode, err)
	}

	return entry.toDomainURL(), nil
}

// Set stores a URL in the cache with the given TTL in seconds.
//
// TTL design rationale:
//
//	A TTL of 3600s (1 hour) means a URL update takes up to 1 hour to
//	propagate to all redirect requests (cache staleness window).
//	For most URL shortener use cases this is acceptable. If lower staleness
//	is required, call Delete() explicitly after an Update() operation,
//	which the application layer does in Story 1.5.
//
//	We use SET with EX (expiry) rather than SET + EXPIRE to ensure the
//	key and TTL are set atomically. A crash between SET and EXPIRE would
//	leave a key with no TTL — effectively permanent — causing memory leaks.
func (c *URLCache) Set(ctx context.Context, u *domainurl.URL, ttlSeconds int) error {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "URLCache.Set",
		trace.WithAttributes(
			attribute.String("cache.operation", "SET"),
			attribute.String("url.short_code", u.ShortCode),
			attribute.Int("cache.ttl_seconds", ttlSeconds),
		),
	)
	defer span.End()

	entry := toCacheEntry(u)

	data, err := json.Marshal(entry)
	if err != nil {
		span.RecordError(err)
		return fmt.Errorf("redis: serializing cache entry for %s: %w", u.ShortCode, err)
	}

	key := positiveKeyPrefix + u.ShortCode
	ttl := time.Duration(ttlSeconds) * time.Second

	if err := c.client.rdb.Set(ctx, key, data, ttl).Err(); err != nil {
		span.RecordError(err)
		return fmt.Errorf("redis: SET %s: %w", key, err)
	}

	return nil
}

// SetNotFound caches a negative result — "this short code does not exist".
//
// Negative caching prevents cache stampede on non-existent short codes.
// Without it, every request for "randomgarbage" hits PostgreSQL. With a
// viral link sharing a bad URL, this can generate thousands of DB queries
// per second for a key that will never exist.
//
// We use a sentinel value ("1") rather than storing actual data —
// we only need to know whether the key exists, not what it contains.
//
// Negative TTL is intentionally shorter than positive TTL (default 60s vs 3600s):
//   - If a short code is created after a negative cache entry, we want
//     it to become resolvable within 60s, not 1 hour.
//   - Short codes are rarely created for the same value that was just 404'd,
//     so 60s is a reasonable staleness window for negative entries.
func (c *URLCache) SetNotFound(ctx context.Context, shortCode string, ttlSeconds int) error {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "URLCache.SetNotFound",
		trace.WithAttributes(
			attribute.String("cache.operation", "SET_NOTFOUND"),
			attribute.String("url.short_code", shortCode),
			attribute.Int("cache.ttl_seconds", ttlSeconds),
		),
	)
	defer span.End()

	key := negativeKeyPrefix + shortCode
	ttl := time.Duration(ttlSeconds) * time.Second

	if err := c.client.rdb.Set(ctx, key, "1", ttl).Err(); err != nil {
		span.RecordError(err)
		return fmt.Errorf("redis: SET notfound %s: %w", key, err)
	}

	return nil
}

// IsNotFound checks whether a negative cache entry exists for a short code.
//
// Returns:
//
//	(true,  nil) — negative cache HIT: short code is known to not exist
//	(false, nil) — negative cache MISS: we don't know — check the database
//	(false, err) — infrastructure error
//
// Redirect handler usage:
//  1. Check positive cache (Get)       → HIT: redirect immediately
//  2. Check negative cache (IsNotFound) → HIT: return 404 without hitting DB
//  3. Both miss → query PostgreSQL
func (c *URLCache) IsNotFound(ctx context.Context, shortCode string) (bool, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "URLCache.IsNotFound",
		trace.WithAttributes(
			attribute.String("cache.operation", "GET_NOTFOUND"),
			attribute.String("url.short_code", shortCode),
		),
	)
	defer span.End()

	key := negativeKeyPrefix + shortCode

	exists, err := c.client.rdb.Exists(ctx, key).Result()
	if err != nil {
		span.RecordError(err)
		return false, fmt.Errorf("redis: EXISTS %s: %w", key, err)
	}

	found := exists > 0
	span.SetAttributes(attribute.Bool("cache.negative_hit", found))

	return found, nil
}

// Delete removes the positive cache entry for a short code.
// Called by the application layer after a URL is updated or deleted
// to ensure the next redirect request fetches fresh data from PostgreSQL.
//
// Cache invalidation is best-effort — if Redis is down when an update
// occurs, the stale entry will expire naturally at its TTL.
// We log the error but do not fail the update operation because of it.
//
// This is an explicit design decision: cache consistency is eventual,
// not strict. The PRD does not require immediate cache invalidation —
// it requires redirect latency SLOs which this model satisfies.
func (c *URLCache) Delete(ctx context.Context, shortCode string) error {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "URLCache.Delete",
		trace.WithAttributes(
			attribute.String("cache.operation", "DEL"),
			attribute.String("url.short_code", shortCode),
		),
	)
	defer span.End()

	// Delete both the positive and negative cache entries in a single
	// DEL command (Redis DEL accepts multiple keys atomically).
	positiveKey := positiveKeyPrefix + shortCode
	negativeKey := negativeKeyPrefix + shortCode

	if err := c.client.rdb.Del(ctx, positiveKey, negativeKey).Err(); err != nil {
		span.RecordError(err)
		return fmt.Errorf("redis: DEL %s: %w", shortCode, err)
	}

	return nil
}

// TTL returns the remaining TTL in seconds for a cached short code.
// Returns -1 if the key has no TTL, -2 if the key does not exist.
// Used primarily for debugging and in integration tests.
func (c *URLCache) TTL(ctx context.Context, shortCode string) (time.Duration, error) {
	key := positiveKeyPrefix + shortCode
	ttl, err := c.client.rdb.TTL(ctx, key).Result()
	if err != nil {
		return 0, fmt.Errorf("redis: TTL %s: %w", key, err)
	}
	return ttl, nil
}
