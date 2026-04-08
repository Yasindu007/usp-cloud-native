// Package resolve contains the ResolveURL use case — the read-side query
// that accepts a short code and returns the original URL for redirect.
//
// This is the highest-volume operation in the platform (~90% of all traffic).
// Every design decision in this package prioritises latency over consistency.
package resolve

// Query carries the inputs for the ResolveURL use case.
// Intentionally minimal — the redirect service only needs the short code.
// No auth is required for resolution (public endpoint per PRD FR-RDR-01).
type Query struct {
	// ShortCode is the 3–32 character code segment from the request path.
	// Example: for "https://s.example.com/abc1234", ShortCode = "abc1234"
	ShortCode string

	// RequestMetadata carries non-functional data for analytics event emission.
	// The resolve handler emits this asynchronously — it never blocks the redirect.
	// Populated by the redirect HTTP handler from request headers.
	RequestMetadata RequestMetadata
}

// RequestMetadata holds HTTP request context used for analytics event capture.
// It is decoupled from the redirect resolution logic so that analytics
// failures never affect redirect latency or availability.
type RequestMetadata struct {
	// IPAddress is the client IP (after RealIP middleware resolves X-Forwarded-For).
	// Phase 1: stored for analytics. Phase 2: hashed with daily salt before storage.
	IPAddress string

	// UserAgent is the raw User-Agent header value.
	// Used for device type and browser family detection.
	UserAgent string

	// Referrer is the Referer header value (note: HTTP spec misspelling).
	// Used for referrer domain analytics.
	Referrer string

	// RequestID is the correlation ID from the RequestID middleware.
	// Included in analytics events for trace correlation.
	RequestID string
}

// Result is the successful outcome of the ResolveURL use case.
type Result struct {
	// OriginalURL is the target URL for the HTTP 302 redirect.
	OriginalURL string

	// ShortCode is echoed back for logging and analytics.
	ShortCode string

	// WorkspaceID is used by analytics capture to attribute redirect traffic
	// to the owning workspace.
	WorkspaceID string

	// CacheStatus indicates whether this resolution was served from cache.
	// Values: "hit" | "miss" | "negative_hit"
	// Used to populate the cache_hit_ratio SLI metric.
	CacheStatus string
}
