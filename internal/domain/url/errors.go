package url

import "errors"

// Domain errors are sentinel values defined at the domain layer.
// They are implementation-agnostic — the infrastructure layer translates
// driver-specific errors (e.g., pgx.ErrNoRows) into these domain errors.
//
// The HTTP handler layer translates domain errors to HTTP status codes.
// This layered translation keeps HTTP concerns out of the domain and
// database concerns out of the handler.
//
// Error mapping example:
//
//	infrastructure/postgres: pgx.ErrNoRows       → domain.ErrNotFound
//	interfaces/http handler: domain.ErrNotFound  → HTTP 404
//	interfaces/http handler: domain.ErrConflict  → HTTP 409
var (
	// ErrNotFound is returned when a URL cannot be found by its lookup key.
	ErrNotFound = errors.New("url: not found")

	// ErrDeleted is returned when a URL exists but has been soft-deleted.
	// Distinct from ErrNotFound to support audit logging of access attempts
	// on deleted resources.
	ErrDeleted = errors.New("url: has been deleted")

	// ErrExpired is returned when a URL exists but has passed its expiry.
	// The HTTP layer translates this to 410 Gone (per PRD FR-URL-03).
	ErrExpired = errors.New("url: has expired")

	// ErrConflict is returned when a short code already exists.
	// The application layer retries with a new code up to 3 times.
	ErrConflict = errors.New("url: short code already exists")

	// ErrShortCodeRequired is a validation error.
	ErrShortCodeRequired = errors.New("url: short code is required")

	// ErrShortCodeLength is returned when a custom short code is out of bounds.
	ErrShortCodeLength = errors.New("url: short code length must be between 3 and 32 characters")

	// ErrOriginalURLRequired is a validation error.
	ErrOriginalURLRequired = errors.New("url: original URL is required")

	// ErrURLTooLong is returned when the original URL exceeds 8192 characters.
	ErrURLTooLong = errors.New("url: original URL exceeds maximum length of 8192 characters")

	// ErrInvalidURL is returned when the URL fails RFC 3986 parsing.
	ErrInvalidURL = errors.New("url: original URL is not a valid URL")

	// ErrWorkspaceIDRequired is a validation error.
	ErrWorkspaceIDRequired = errors.New("url: workspace ID is required")

	// ErrUnauthorized is returned when a user attempts to access a URL
	// that belongs to a different workspace.
	ErrUnauthorized = errors.New("url: unauthorized access to resource")
)

// IsNotFound returns true if the error is or wraps ErrNotFound or ErrDeleted.
// Use this in application-layer handlers to avoid importing domain errors directly.
func IsNotFound(err error) bool {
	return errors.Is(err, ErrNotFound) || errors.Is(err, ErrDeleted)
}

// IsGone returns true if the error is or wraps ErrExpired.
// Maps to HTTP 410 Gone.
func IsGone(err error) bool {
	return errors.Is(err, ErrExpired)
}

// IsConflict returns true if the error is or wraps ErrConflict.
// Maps to HTTP 409 Conflict.
func IsConflict(err error) bool {
	return errors.Is(err, ErrConflict)
}
