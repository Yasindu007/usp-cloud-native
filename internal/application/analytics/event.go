// Package analytics defines the domain model for URL redirect analytics.
//
// Design philosophy:
//
//	Analytics data is append-only event data — never updated, retained for
//	compliance, queried for aggregate insights. The domain model reflects this:
//	RedirectEvent is a value object (no mutable methods), constructed once
//	at capture time and never modified.
//
// Privacy by design (PRD section 14.1):
//   - Raw IP addresses are NEVER stored in this struct or in the database
//   - ip_hash is SHA-256(ip_address + daily_salt) — a pseudonymous identifier
//   - Changing the salt daily means: same IP on different days → different hashes
//   - Cross-day visitor tracking via IP is impossible by construction
//
// Relationship to click_count on urls table:
//
//	The urls.click_count column is a denormalized counter incremented
//	atomically via UPDATE ... SET click_count = click_count + 1.
//	The redirect_events table is the authoritative, event-level record.
//	click_count is a fast-read approximation; redirect_events supports
//	the full analytics query API (time-series, geo breakdown, device breakdown).
//	The ingestion service updates BOTH: one batch INSERT into redirect_events
//	and one batch UPDATE on urls.click_count per unique short_code.
package analytics

import "time"

// DeviceType classifies the client device category.
type DeviceType string

const (
	DeviceTypeMobile  DeviceType = "mobile"
	DeviceTypeDesktop DeviceType = "desktop"
	DeviceTypeTablet  DeviceType = "tablet"
	DeviceTypeBot     DeviceType = "bot"
	DeviceTypeUnknown DeviceType = "unknown"
)

// RedirectEvent represents a single redirect resolution event.
// Captured asynchronously from the redirect service hot path.
// Every field is set at construction time — no mutation after creation.
type RedirectEvent struct {
	// ID is the ULID for this event.
	// ULIDs provide time-ordering within a partition without an additional index.
	ID string

	// ShortCode is the short code that was resolved.
	// Stored directly (not a FK) for query performance and retention independence.
	ShortCode string

	// WorkspaceID is the owning workspace's ULID.
	// Stored for workspace-level analytics queries.
	WorkspaceID string

	// OccurredAt is when the redirect was served (microsecond precision, UTC).
	// This is the partition key — must be accurate.
	OccurredAt time.Time

	// IPHash is SHA-256(raw_ip + daily_salt).
	// Empty string for bot traffic (bots don't need visitor counting).
	// The salt rotates daily — see pkg/iphasher for the hashing logic.
	IPHash string

	// UserAgent is the raw User-Agent header value.
	// Stored for UA breakdown and future re-parsing if classification rules change.
	// Truncated to 512 chars to bound storage.
	UserAgent string

	// DeviceType is the classified device category (parsed from UserAgent).
	DeviceType DeviceType

	// BrowserFamily is the browser name (e.g., "Chrome", "Firefox", "Safari").
	// "bot" for known bots, "unknown" when parsing fails.
	BrowserFamily string

	// OSFamily is the operating system name (e.g., "Windows", "macOS", "Android").
	OSFamily string

	// IsBot is true when the User-Agent matches a known bot pattern.
	// Bot events are stored but excluded from analytics counts by default.
	IsBot bool

	// CountryCode is the ISO 3166-1 alpha-2 country code from GeoIP lookup.
	// "XX" when the IP cannot be geolocated (private ranges, VPNs, unknown).
	// Phase 3: stub always returns "XX". Phase 4: real MaxMind DB lookup.
	CountryCode string

	// ReferrerDomain is the domain portion of the Referer header.
	// "direct" when no Referer header is present.
	// "unknown" when the header is present but cannot be parsed.
	// Only the domain is stored — full URL referrers contain PII (query params).
	ReferrerDomain string

	// ReferrerRaw is the full Referer header, truncated to 1024 chars.
	// Used for debugging and future referrer analysis.
	ReferrerRaw string

	// RequestID is the X-Request-ID for correlating analytics events with
	// application logs and distributed traces.
	RequestID string
}
