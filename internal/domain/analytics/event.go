// Package analytics defines the domain model for URL redirect analytics.
package analytics

import (
	"fmt"
	"strings"
	"time"
)

// DeviceType classifies the client device category.
type DeviceType string

const (
	DeviceTypeMobile  DeviceType = "mobile"
	DeviceTypeDesktop DeviceType = "desktop"
	DeviceTypeTablet  DeviceType = "tablet"
	DeviceTypeBot     DeviceType = "bot"
	DeviceTypeUnknown DeviceType = "unknown"

	// ChannelPrefix is the Redis Pub/Sub namespace for real-time click events.
	ChannelPrefix = "click"
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

// ClickEvent is the lean real-time event published to Redis and streamed to SSE clients.
// It intentionally excludes raw IP data and heavyweight parsed fields used only by batch analytics.
type ClickEvent struct {
	ShortCode   string    `json:"short_code"`
	WorkspaceID string    `json:"workspace_id"`
	OccurredAt  time.Time `json:"occurred_at"`
	Referrer    string    `json:"referrer,omitempty"`
	DeviceType  string    `json:"device_type"`
	CountryCode string    `json:"country_code"`
	IsBot       bool      `json:"is_bot"`
	RequestID   string    `json:"request_id"`
}

// Channel returns the Redis channel for this event.
func (e *ClickEvent) Channel() string {
	return ChannelName(e.WorkspaceID, e.ShortCode)
}

// ChannelName returns the Pub/Sub channel for one URL's click stream.
func ChannelName(workspaceID, shortCode string) string {
	return fmt.Sprintf("%s:%s:%s", ChannelPrefix, workspaceID, shortCode)
}

// WorkspacePattern returns the Pub/Sub pattern for all clicks in a workspace.
func WorkspacePattern(workspaceID string) string {
	return fmt.Sprintf("%s:%s:*", ChannelPrefix, workspaceID)
}

// ClassifyDevice categorizes a user-agent for the real-time click stream.
func ClassifyDevice(userAgent string) (DeviceType, bool) {
	ua := strings.ToLower(userAgent)

	botSignals := []string{
		"googlebot", "bingbot", "slurp", "duckduckbot", "baiduspider",
		"yandexbot", "sogou", "facebookexternalhit", "twitterbot",
		"rogerbot", "linkedinbot", "embedly", "quora link preview",
		"showyoubot", "outbrain", "pinterest/0.", "developers.google.com",
		"applebot", "whatsapp", "semrushbot", "ahrefsbot", "mj12bot",
		"dotbot", "archive.org_bot", "curl/", "python-requests", "go-http-client",
		"wget/", "scrapy/", "headlesschrome", "phantomjs",
	}
	for _, signal := range botSignals {
		if strings.Contains(ua, signal) {
			return DeviceTypeBot, true
		}
	}

	if strings.Contains(ua, "ipad") ||
		(strings.Contains(ua, "android") && !strings.Contains(ua, "mobile")) ||
		strings.Contains(ua, "tablet") {
		return DeviceTypeTablet, false
	}

	mobileSignals := []string{
		"mobile", "iphone", "ipod", "android",
		"blackberry", "windows phone", "opera mini",
	}
	for _, signal := range mobileSignals {
		if strings.Contains(ua, signal) {
			return DeviceTypeMobile, false
		}
	}

	if ua == "" {
		return DeviceTypeUnknown, false
	}

	return DeviceTypeDesktop, false
}

// ExtractReferrerDomain strips a referrer URL down to its host to avoid leaking paths or query data.
func ExtractReferrerDomain(referrer string) string {
	if referrer == "" {
		return ""
	}

	r := referrer
	if idx := strings.Index(r, "://"); idx >= 0 {
		r = r[idx+3:]
	}
	if idx := strings.IndexAny(r, "/?#"); idx >= 0 {
		r = r[:idx]
	}
	if idx := strings.LastIndex(r, ":"); idx >= 0 {
		r = r[:idx]
	}
	return strings.ToLower(strings.TrimSpace(r))
}
