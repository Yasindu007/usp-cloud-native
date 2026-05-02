// Package infrastructure contains all driven adapters (secondary adapters)
// that implement the ports defined in the domain layer.
//
// Adapters in this package:
//   - postgres/   — Implements domain/url.Repository using pgx v5
//   - redis/      — Implements domain/url.CachePort using go-redis v9
//   - metrics/    — Prometheus metric registrations
//
// Dependency direction: infrastructure → domain (never the reverse).
// The domain defines interfaces; infrastructure implements them.
//
// Infrastructure errors are translated to domain errors at the adapter boundary.
// Example: pgx.ErrNoRows → domain/url.ErrNotFound
// This keeps database error types from leaking into business logic.
//
// Populated in Story 1.3 (PostgreSQL) and Story 1.4 (Redis).
package infrastructure
