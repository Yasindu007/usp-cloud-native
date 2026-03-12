package url

import "context"

// Repository defines the write-side persistence contract for URL entities.
// This is a "driven port" (secondary port) in ports-and-adapters terminology.
//
// The interface is defined in the domain layer, implemented in the
// infrastructure/postgres package. This inversion of dependency means
// the domain never imports pgx — swapping storage is a configuration change,
// not a domain change.
//
// All methods accept a context.Context for:
//   - Cancellation propagation (request timeout hits DB query)
//   - Trace span propagation (OTel spans flow through context)
//   - Deadline enforcement (DB query deadline = HTTP request deadline)
type Repository interface {
	// Create persists a new URL entity. The caller is responsible for
	// generating the ULID ID and short code before calling this method.
	Create(ctx context.Context, u *URL) error

	// GetByShortCode retrieves a URL by its short code.
	// Returns ErrNotFound if no URL with that short code exists.
	// Returns ErrDeleted if the URL was soft-deleted.
	GetByShortCode(ctx context.Context, shortCode string) (*URL, error)

	// GetByID retrieves a URL by its ULID.
	// Returns ErrNotFound if no URL with that ID exists in the workspace.
	GetByID(ctx context.Context, id string, workspaceID string) (*URL, error)

	// Update persists mutations to an existing URL.
	// Only non-zero fields in the provided struct are updated (partial update).
	// UpdatedAt is always set to now() by the implementation.
	Update(ctx context.Context, u *URL) error

	// SoftDelete marks a URL as deleted without removing the row.
	// The URL becomes unresolvable immediately (redirect returns 404).
	// Hard deletion happens via a scheduled purge job (90-day retention).
	SoftDelete(ctx context.Context, id string, workspaceID string) error

	// List returns a paginated list of URLs for a workspace.
	// Returns the matching URLs and the cursor for the next page.
	// An empty nextCursor string indicates the last page.
	List(ctx context.Context, filter ListFilter) ([]*URL, string, error)

	// IncrementClickCount atomically increments the click counter.
	// This is a fire-and-forget operation in the redirect hot path —
	// use the async path (Story 1.5) rather than blocking on this.
	IncrementClickCount(ctx context.Context, shortCode string) error
}

// ReadonlyRepository defines the read-only contract used by the redirect service.
// Keeping this separate from Repository enforces the single-responsibility principle:
// the redirect service should never have write access to the URL store.
// In Kubernetes, the redirect service's service account only gets read access.
type ReadonlyRepository interface {
	GetByShortCode(ctx context.Context, shortCode string) (*URL, error)
}

// CachePort defines the caching contract for URL resolution.
// Implemented by infrastructure/redis. Used by the redirect service
// to serve responses without hitting PostgreSQL.
//
// Cache semantics:
//   - Cache stores the full URL entity (marshalled as JSON)
//   - TTL is configurable per entry (default 3600s from config)
//   - Negative cache: cache "not found" with a shorter TTL to prevent
//     DB hammering from requests for non-existent short codes
type CachePort interface {
	// Get returns a cached URL. Returns (nil, nil) on cache miss.
	// Returns (nil, err) on infrastructure failure.
	Get(ctx context.Context, shortCode string) (*URL, error)

	// Set stores a URL in the cache with the given TTL.
	Set(ctx context.Context, u *URL, ttlSeconds int) error

	// SetNotFound caches a negative result (short code not found) with
	// a shorter TTL to prevent stampede on non-existent codes.
	SetNotFound(ctx context.Context, shortCode string, ttlSeconds int) error

	// IsNotFound returns true if the cached value indicates absence.
	// This distinguishes a cache miss from a cached "not found".
	IsNotFound(ctx context.Context, shortCode string) (bool, error)

	// Delete invalidates the cache entry for a short code.
	// Called when a URL is updated or deleted.
	Delete(ctx context.Context, shortCode string) error
}

// ListFilter defines the query parameters for listing URLs.
// Using a struct rather than variadic options makes the API explicit
// and prevents parameter order bugs.
type ListFilter struct {
	WorkspaceID string
	Status      *Status    // nil = all statuses
	CreatedBy   *string    // nil = all creators
	Cursor      string     // empty = first page
	Limit       int        // 0 = default (20), max 100
}