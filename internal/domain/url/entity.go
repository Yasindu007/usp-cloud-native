// Package url defines the core domain model for the URL shortening bounded context.
//
// Clean Architecture Domain Layer rules:
//   - No imports from application, infrastructure, or interfaces layers
//   - No framework dependencies (no chi, no pgx, no redis)
//   - No I/O operations
//   - Business rules live here as methods on entities
//
// This package is the most stable in the system. It changes only when
// business rules change, not when we swap databases or HTTP frameworks.
package url

import (
	"net/url"
	"time"
)

// Status represents the lifecycle state of a shortened URL.
// Using a named string type (not iota int) makes database queries,
// logs, and API responses human-readable without a lookup table.
type Status string

const (
	// StatusActive is the default state — the URL can be resolved.
	StatusActive Status = "active"

	// StatusExpired is set by the expiration worker when expires_at is past.
	// Redirect attempts return HTTP 410 Gone.
	StatusExpired Status = "expired"

	// StatusDisabled is an admin or owner action. Redirect returns 404.
	StatusDisabled Status = "disabled"

	// StatusDeleted is the soft-delete state. Redirect returns 404.
	// Records are purged after the retention period (90 days per PRD 5.1.6).
	StatusDeleted Status = "deleted"
)

// URL is the aggregate root of the URL shortening domain.
// Fields map directly to the PRD data model (Section 9.1).
//
// ID uses ULID format: lexicographically sortable, URL-safe, and globally unique.
// This enables cursor-based pagination and efficient B-tree indexing.
type URL struct {
	ID          string     // ULID
	WorkspaceID string     // ULID of the owning workspace
	ShortCode   string     // Base62 short code, globally unique
	OriginalURL string     // Full original URL (RFC 3986, max 8192 chars)
	Title       string     // Optional human-readable display name
	Status      Status     // Current lifecycle status
	ExpiresAt   *time.Time // nil = no expiry
	CreatedBy   string     // ULID of the creating user
	CreatedAt   time.Time
	UpdatedAt   time.Time
	DeletedAt   *time.Time // nil = not deleted; set on soft delete
	ClickCount  int64      // Denormalized counter — incremented on redirect
}

// IsExpired reports whether the URL has passed its expiration time.
// A URL with a nil ExpiresAt never expires.
//
// SRE note: This check is duplicated in both application and DB query layers.
// The DB query uses WHERE expires_at IS NULL OR expires_at > NOW() as the
// authoritative filter. This method is used for in-memory validation only.
func (u *URL) IsExpired() bool {
	if u.ExpiresAt == nil {
		return false
	}
	return time.Now().UTC().After(*u.ExpiresAt)
}

// IsActive reports whether the URL is in the active state.
// Does not account for expiry — use CanRedirect for the combined check.
func (u *URL) IsActive() bool {
	return u.Status == StatusActive
}

// CanRedirect reports whether the URL can currently serve a redirect.
// This is the authoritative domain rule for redirect eligibility.
// It combines status check and expiration check.
func (u *URL) CanRedirect() bool {
	return u.Status == StatusActive && !u.IsExpired()
}

// Validate performs domain-level validation of the URL entity.
// This is called before persisting to ensure invariants are maintained
// regardless of which storage adapter is in use.
func (u *URL) Validate() error {
	if u.ShortCode == "" {
		return ErrShortCodeRequired
	}
	if len(u.ShortCode) < MinShortCodeLength || len(u.ShortCode) > MaxShortCodeLength {
		return ErrShortCodeLength
	}
	if u.OriginalURL == "" {
		return ErrOriginalURLRequired
	}
	if len(u.OriginalURL) > MaxURLLength {
		return ErrURLTooLong
	}
	if _, err := url.ParseRequestURI(u.OriginalURL); err != nil {
		return ErrInvalidURL
	}
	if u.WorkspaceID == "" {
		return ErrWorkspaceIDRequired
	}
	return nil
}

// Domain constants — centralizing these here prevents magic numbers
// from leaking into the application and infrastructure layers.
const (
	MinShortCodeLength = 3
	MaxShortCodeLength = 32
	MaxURLLength       = 8192
)
