// Package analytics defines the domain model for analytics query operations.
//
// This package owns the vocabulary of analytics queries — what time windows
// are valid, what granularities are supported, what dimensions can be broken
// down. The query constraints here map directly to PRD section 5.4.
//
// Separation from the ingestion domain (event.go in Story 3.1):
//
//	Ingestion is a write concern — optimised for throughput, buffered, async.
//	Querying is a read concern — optimised for latency, uses the read replica,
//	benefits from pre-computed recording rules in Prometheus for dashboards.
//	Keeping them in separate files prevents write-path types from leaking
//	into read-path handlers and vice versa.
package analytics

import (
	"errors"
	"time"
)

// ── Time windows ──────────────────────────────────────────────────────────────

// Window represents a named time window for analytics aggregation.
// Named windows map to concrete durations and are used by the summary endpoint.
type Window string

const (
	Window1Hour   Window = "1h"
	Window24Hour  Window = "24h"
	Window7Day    Window = "7d"
	Window30Day   Window = "30d"
	WindowAllTime Window = "all"
)

// Duration returns the time.Duration for a named window.
// WindowAllTime returns 0 (caller interprets as unbounded).
func (w Window) Duration() time.Duration {
	switch w {
	case Window1Hour:
		return time.Hour
	case Window24Hour:
		return 24 * time.Hour
	case Window7Day:
		return 7 * 24 * time.Hour
	case Window30Day:
		return 30 * 24 * time.Hour
	default:
		return 0
	}
}

// IsValid returns true for recognised window values.
func (w Window) IsValid() bool {
	switch w {
	case Window1Hour, Window24Hour, Window7Day, Window30Day, WindowAllTime:
		return true
	}
	return false
}

// ParseWindow converts a string to a Window, returning an error for
// unrecognised values. Used by HTTP handlers to parse query parameters.
func ParseWindow(s string) (Window, error) {
	w := Window(s)
	if !w.IsValid() {
		return "", errors.New("analytics: window must be one of: 1h, 24h, 7d, 30d, all")
	}
	return w, nil
}

// ── Granularity ───────────────────────────────────────────────────────────────

// Granularity controls the bucket size for time-series queries.
// PRD section 5.4.2 defines the valid combinations:
//
//	1-minute buckets: max 24h window
//	1-hour buckets:   max 30d window
//	1-day buckets:    max 365d window
type Granularity string

const (
	Granularity1Minute Granularity = "1m"
	Granularity1Hour   Granularity = "1h"
	Granularity1Day    Granularity = "1d"
)

// IsValid returns true for recognised granularity values.
func (g Granularity) IsValid() bool {
	switch g {
	case Granularity1Minute, Granularity1Hour, Granularity1Day:
		return true
	}
	return false
}

// ParseGranularity converts a string to a Granularity.
func ParseGranularity(s string) (Granularity, error) {
	g := Granularity(s)
	if !g.IsValid() {
		return "", errors.New("analytics: granularity must be one of: 1m, 1h, 1d")
	}
	return g, nil
}

// ValidateWindowGranularity enforces PRD section 5.4.2 compatibility rules.
// Returns an error if the window is too wide for the requested granularity.
func ValidateWindowGranularity(start, end time.Time, g Granularity) error {
	span := end.Sub(start)
	switch g {
	case Granularity1Minute:
		if span > 24*time.Hour {
			return errors.New("analytics: 1-minute granularity requires a window of 24 hours or less")
		}
	case Granularity1Hour:
		if span > 30*24*time.Hour {
			return errors.New("analytics: 1-hour granularity requires a window of 30 days or less")
		}
	case Granularity1Day:
		if span > 365*24*time.Hour {
			return errors.New("analytics: 1-day granularity requires a window of 365 days or less")
		}
	}
	return nil
}

// ── Dimension ─────────────────────────────────────────────────────────────────

// Dimension is a categorical field for breakdown queries.
type Dimension string

const (
	DimensionCountry    Dimension = "country"
	DimensionDeviceType Dimension = "device_type"
	DimensionBrowser    Dimension = "browser_family"
	DimensionOS         Dimension = "os_family"
	DimensionReferrer   Dimension = "referrer_domain"
)

// IsValid returns true for recognised dimension values.
func (d Dimension) IsValid() bool {
	switch d {
	case DimensionCountry, DimensionDeviceType, DimensionBrowser,
		DimensionOS, DimensionReferrer:
		return true
	}
	return false
}

// ColumnName returns the database column name for this dimension.
// Used to safely construct GROUP BY clauses — dimension values are
// validated before use, preventing SQL injection via dimension names.
func (d Dimension) ColumnName() string {
	switch d {
	case DimensionCountry:
		return "country_code"
	case DimensionDeviceType:
		return "device_type"
	case DimensionBrowser:
		return "browser_family"
	case DimensionOS:
		return "os_family"
	case DimensionReferrer:
		return "referrer_domain"
	default:
		return ""
	}
}

// ParseDimension converts a string to a Dimension.
func ParseDimension(s string) (Dimension, error) {
	d := Dimension(s)
	if !d.IsValid() {
		return "", errors.New("analytics: dimension must be one of: country, device_type, browser_family, os_family, referrer_domain")
	}
	return d, nil
}

// ── Result types ──────────────────────────────────────────────────────────────

// Summary is the aggregate click count for a URL over a time window.
// Returned by GET /api/v1/workspaces/{id}/urls/{urlID}/analytics
type Summary struct {
	ShortCode   string
	TotalClicks int64
	UniqueIPs   int64 // approximate (COUNT DISTINCT on hashed IP)
	WindowStart time.Time
	WindowEnd   time.Time
	// BotClicks is the count of requests identified as bot traffic.
	// Excluded from TotalClicks by default (is_bot = false filter).
	BotClicks int64
}

// TimeSeriesPoint is a single time bucket with its click count.
type TimeSeriesPoint struct {
	// BucketStart is the start of the time bucket (UTC).
	BucketStart time.Time
	Clicks      int64
	UniqueIPs   int64
}

// TimeSeries is the ordered sequence of time buckets for a URL.
// Returned by GET /api/v1/workspaces/{id}/urls/{urlID}/analytics/timeseries
type TimeSeries struct {
	ShortCode   string
	Granularity Granularity
	WindowStart time.Time
	WindowEnd   time.Time
	Points      []TimeSeriesPoint
}

// DimensionCount is a single dimension value with its click count.
type DimensionCount struct {
	// Value is the dimension value (e.g. "US", "mobile", "Chrome").
	// Empty string represents unclassified/unknown.
	Value  string
	Clicks int64
	// Percentage is Clicks / TotalClicks * 100, computed at the query layer.
	Percentage float64
}

// Breakdown is the dimensional distribution of clicks for a URL.
// Returned by GET /api/v1/workspaces/{id}/urls/{urlID}/analytics/breakdown
type Breakdown struct {
	ShortCode   string
	Dimension   Dimension
	WindowStart time.Time
	WindowEnd   time.Time
	TotalClicks int64
	Counts      []DimensionCount
}
