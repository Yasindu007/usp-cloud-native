// Package shorten contains the ShortenURL use case — the write-side
// command that accepts a long URL and returns a shortened one.
//
// CQRS separation rationale:
//
//	The shorten (write) and resolve (read) operations have fundamentally
//	different characteristics:
//
//	Shorten: low volume (~10% of traffic), writes to primary DB,
//	         involves validation, collision retry, cache pre-warm.
//	Resolve: high volume (~90% of traffic), reads from cache first,
//	         sub-millisecond path when cached, async side effects.
//
//	Separating them into distinct packages makes their contracts explicit,
//	prevents accidental coupling (e.g., a resolve handler accidentally
//	writing to the DB), and allows independent testing.
package shorten

import "time"

// Command carries all inputs required to create a shortened URL.
// It is the boundary object between the HTTP handler (interfaces layer)
// and the application use case. The HTTP handler is responsible for
// deserializing the HTTP request into a Command; the handler is
// responsible for nothing beyond orchestration.
//
// Validation philosophy:
//   Structural validation (is this a non-empty string?) belongs in the
//   HTTP handler or input parsing layer.
//   Business validation (is this URL scheme allowed? is the short code
//   reserved?) belongs in this use case handler.
//   Domain invariant enforcement (click_count >= 0) belongs in the entity.
type Command struct {
	// OriginalURL is the long URL to be shortened.
	// Must be a valid RFC 3986 URL with http or https scheme.
	// Max length: 8192 characters (PRD section 9.1).
	OriginalURL string

	// CustomCode is an optional caller-specified short code.
	// When empty, the handler generates a cryptographically random Base62 code.
	// When set, the handler validates it against reserved paths and profanity list.
	// Constraints: alphanumeric + hyphen + underscore, 3–32 characters.
	CustomCode string

	// WorkspaceID is the ULID of the workspace that owns this URL.
	// Populated by the auth middleware from the validated JWT claims.
	// Required — no URL can exist without a workspace.
	WorkspaceID string

	// CreatedBy is the ULID of the authenticated user creating the URL.
	// Populated by the auth middleware.
	CreatedBy string

	// Title is an optional human-readable label for the URL.
	// Displayed in the dashboard. Not used for redirect logic.
	Title string

	// ExpiresAt is the optional expiration time for the short URL.
	// After this time, redirects return HTTP 410 Gone.
	// nil means no expiry (indefinite).
	ExpiresAt *time.Time
}

// Result is the successful outcome of the ShortenURL use case.
// Returned to the HTTP handler which serializes it into a JSON response.
type Result struct {
	// ShortURL is the full publicly accessible short URL.
	// Formed as: BaseURL + "/" + ShortCode
	// Example: "https://s.example.com/abc1234"
	ShortURL string

	// ShortCode is the generated or user-specified code segment.
	ShortCode string

	// ID is the ULID of the created URL record.
	// Used by the client for subsequent CRUD operations (GET, PATCH, DELETE).
	ID string

	// OriginalURL is echoed back for client confirmation.
	OriginalURL string

	// WorkspaceID is echoed back for the client.
	WorkspaceID string

	// CreatedAt is the timestamp when the record was persisted.
	CreatedAt string // RFC 3339 formatted
}
