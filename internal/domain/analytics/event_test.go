package analytics_test

import (
	"testing"

	"github.com/urlshortener/platform/internal/domain/analytics"
)

func TestChannelName(t *testing.T) {
	got := analytics.ChannelName("ws_001", "abc1234")
	if got != "click:ws_001:abc1234" {
		t.Fatalf("ChannelName() = %q", got)
	}
}

func TestWorkspacePattern(t *testing.T) {
	got := analytics.WorkspacePattern("ws_001")
	if got != "click:ws_001:*" {
		t.Fatalf("WorkspacePattern() = %q", got)
	}
}

func TestClickEventChannel(t *testing.T) {
	evt := &analytics.ClickEvent{WorkspaceID: "ws_001", ShortCode: "abc1234"}
	if got := evt.Channel(); got != "click:ws_001:abc1234" {
		t.Fatalf("ClickEvent.Channel() = %q", got)
	}
}

func TestClassifyDevice(t *testing.T) {
	tests := []struct {
		name    string
		ua      string
		want    analytics.DeviceType
		wantBot bool
	}{
		{"desktop", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36", analytics.DeviceTypeDesktop, false},
		{"mobile", "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X)", analytics.DeviceTypeMobile, false},
		{"tablet", "Mozilla/5.0 (iPad; CPU OS 17_0 like Mac OS X)", analytics.DeviceTypeTablet, false},
		{"bot", "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)", analytics.DeviceTypeBot, true},
		{"curl-bot", "curl/7.88.1", analytics.DeviceTypeBot, true},
		{"unknown", "", analytics.DeviceTypeUnknown, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, gotBot := analytics.ClassifyDevice(tc.ua)
			if got != tc.want || gotBot != tc.wantBot {
				t.Fatalf("ClassifyDevice(%q) = (%q, %v)", tc.ua, got, gotBot)
			}
		})
	}
}

func TestExtractReferrerDomain(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"https://www.google.com/search?q=test", "www.google.com"},
		{"http://twitter.com/user/status/123", "twitter.com"},
		{"https://example.com:8080/path?q=1#frag", "example.com"},
		{"", ""},
		{"not-a-url", "not-a-url"},
	}

	for _, tc := range tests {
		if got := analytics.ExtractReferrerDomain(tc.in); got != tc.want {
			t.Fatalf("ExtractReferrerDomain(%q) = %q", tc.in, got)
		}
	}
}
