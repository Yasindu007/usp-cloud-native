// Package analytics defines the domain model for analytics query operations.
package analytics

import (
	"errors"
	"time"
)

// Window represents a named time window for analytics aggregation.
type Window string

const (
	Window1Hour   Window = "1h"
	Window24Hour  Window = "24h"
	Window7Day    Window = "7d"
	Window30Day   Window = "30d"
	WindowAllTime Window = "all"
)

// Duration returns the concrete duration for a named window.
// WindowAllTime returns 0 and is handled by callers as unbounded.
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

// IsValid returns true for recognized window values.
func (w Window) IsValid() bool {
	switch w {
	case Window1Hour, Window24Hour, Window7Day, Window30Day, WindowAllTime:
		return true
	default:
		return false
	}
}

// ParseWindow converts a string to a Window.
func ParseWindow(s string) (Window, error) {
	w := Window(s)
	if !w.IsValid() {
		return "", errors.New("analytics: window must be one of: 1h, 24h, 7d, 30d, all")
	}
	return w, nil
}

// Granularity controls the bucket size for time-series queries.
type Granularity string

const (
	Granularity1Minute Granularity = "1m"
	Granularity1Hour   Granularity = "1h"
	Granularity1Day    Granularity = "1d"
)

// IsValid returns true for recognized granularity values.
func (g Granularity) IsValid() bool {
	switch g {
	case Granularity1Minute, Granularity1Hour, Granularity1Day:
		return true
	default:
		return false
	}
}

// ParseGranularity converts a string to a Granularity.
func ParseGranularity(s string) (Granularity, error) {
	g := Granularity(s)
	if !g.IsValid() {
		return "", errors.New("analytics: granularity must be one of: 1m, 1h, 1d")
	}
	return g, nil
}

// ValidateWindowGranularity ensures the requested bucket size fits the time span.
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

// Dimension is a categorical field for breakdown queries.
type Dimension string

const (
	DimensionCountry    Dimension = "country"
	DimensionDeviceType Dimension = "device_type"
	DimensionBrowser    Dimension = "browser_family"
	DimensionOS         Dimension = "os_family"
	DimensionReferrer   Dimension = "referrer_domain"
)

// IsValid returns true for recognized dimension values.
func (d Dimension) IsValid() bool {
	switch d {
	case DimensionCountry, DimensionDeviceType, DimensionBrowser, DimensionOS, DimensionReferrer:
		return true
	default:
		return false
	}
}

// ColumnName returns the database column name for this dimension.
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

// Summary is the aggregate click count for a URL over a time window.
type Summary struct {
	ShortCode   string
	TotalClicks int64
	UniqueIPs   int64
	WindowStart time.Time
	WindowEnd   time.Time
	BotClicks   int64
}

// TimeSeriesPoint is a single time bucket with its click count.
type TimeSeriesPoint struct {
	BucketStart time.Time
	Clicks      int64
	UniqueIPs   int64
}

// TimeSeries is the ordered sequence of time buckets for a URL.
type TimeSeries struct {
	ShortCode   string
	Granularity Granularity
	WindowStart time.Time
	WindowEnd   time.Time
	Points      []TimeSeriesPoint
}

// DimensionCount is a single dimension value with its click count.
type DimensionCount struct {
	Value      string
	Clicks     int64
	Percentage float64
}

// Breakdown is the dimensional distribution of clicks for a URL.
type Breakdown struct {
	ShortCode   string
	Dimension   Dimension
	WindowStart time.Time
	WindowEnd   time.Time
	TotalClicks int64
	Counts      []DimensionCount
}
