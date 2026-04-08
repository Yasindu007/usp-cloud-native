// Package analytics defines the domain model for URL redirect analytics.
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
type RedirectEvent struct {
	ID             string
	ShortCode      string
	WorkspaceID    string
	OccurredAt     time.Time
	IPHash         string
	UserAgent      string
	DeviceType     DeviceType
	BrowserFamily  string
	OSFamily       string
	IsBot          bool
	CountryCode    string
	ReferrerDomain string
	ReferrerRaw    string
	RequestID      string
}
