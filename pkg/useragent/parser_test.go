package useragent_test

import (
	"testing"

	"github.com/urlshortener/platform/pkg/useragent"
)

func TestParse_Desktop_Chrome_Windows(t *testing.T) {
	ua := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
	result := useragent.Parse(ua)

	if result.IsBot {
		t.Error("Chrome on Windows should not be classified as bot")
	}
	if result.DeviceType != "desktop" {
		t.Errorf("expected desktop, got %q", result.DeviceType)
	}
	if result.BrowserFamily != "Chrome" {
		t.Errorf("expected Chrome, got %q", result.BrowserFamily)
	}
	if result.OSFamily != "Windows" {
		t.Errorf("expected Windows, got %q", result.OSFamily)
	}
}

func TestParse_Mobile_Safari_iOS(t *testing.T) {
	ua := "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1"
	result := useragent.Parse(ua)

	if result.IsBot {
		t.Error("Mobile Safari should not be classified as bot")
	}
	if result.DeviceType != "mobile" {
		t.Errorf("expected mobile, got %q", result.DeviceType)
	}
	if result.BrowserFamily != "Safari" {
		t.Errorf("expected Safari, got %q", result.BrowserFamily)
	}
	if result.OSFamily != "iOS" {
		t.Errorf("expected iOS, got %q", result.OSFamily)
	}
}

func TestParse_Tablet_iPad(t *testing.T) {
	ua := "Mozilla/5.0 (iPad; CPU OS 16_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/16.0 Mobile/15E148 Safari/604.1"
	result := useragent.Parse(ua)

	if result.DeviceType != "tablet" {
		t.Errorf("expected tablet for iPad UA, got %q", result.DeviceType)
	}
}

func TestParse_Tablet_AndroidNoMobile(t *testing.T) {
	ua := "Mozilla/5.0 (Linux; Android 13; Pixel Tablet) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36"
	result := useragent.Parse(ua)

	if result.DeviceType != "tablet" {
		t.Errorf("expected tablet for Android without 'Mobile', got %q", result.DeviceType)
	}
}

func TestParse_Mobile_Android_Chrome(t *testing.T) {
	ua := "Mozilla/5.0 (Linux; Android 13; Pixel 7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Mobile Safari/537.36"
	result := useragent.Parse(ua)

	if result.DeviceType != "mobile" {
		t.Errorf("expected mobile, got %q", result.DeviceType)
	}
	if result.OSFamily != "Android" {
		t.Errorf("expected Android, got %q", result.OSFamily)
	}
}

func TestParse_Bot_Googlebot(t *testing.T) {
	ua := "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)"
	result := useragent.Parse(ua)

	if !result.IsBot {
		t.Error("Googlebot must be classified as bot")
	}
	if result.DeviceType != "bot" {
		t.Errorf("expected device_type=bot, got %q", result.DeviceType)
	}
	if result.BrowserFamily != "bot" {
		t.Errorf("expected browser_family=bot, got %q", result.BrowserFamily)
	}
}

func TestParse_Bot_Curl(t *testing.T) {
	ua := "curl/8.1.2"
	result := useragent.Parse(ua)

	if !result.IsBot {
		t.Error("curl should be classified as bot")
	}
}

func TestParse_Bot_PythonRequests(t *testing.T) {
	ua := "python-requests/2.31.0"
	result := useragent.Parse(ua)

	if !result.IsBot {
		t.Error("python-requests should be classified as bot")
	}
}

func TestParse_Bot_GoHTTPClient(t *testing.T) {
	ua := "Go-http-client/2.0"
	result := useragent.Parse(ua)

	if !result.IsBot {
		t.Error("Go-http-client should be classified as bot")
	}
}

func TestParse_EmptyUA_ReturnsUnknown(t *testing.T) {
	result := useragent.Parse("")

	if result.IsBot {
		t.Error("empty UA should not be classified as bot")
	}
	if result.DeviceType != "unknown" {
		t.Errorf("expected unknown for empty UA, got %q", result.DeviceType)
	}
	if result.BrowserFamily != "unknown" {
		t.Errorf("expected unknown browser for empty UA, got %q", result.BrowserFamily)
	}
}

func TestParse_Edge_NotChrome(t *testing.T) {
	ua := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36 Edg/120.0"
	result := useragent.Parse(ua)

	if result.BrowserFamily != "Edge" {
		t.Errorf("expected Edge, got %q — Edge UA also contains Chrome token", result.BrowserFamily)
	}
}

func TestParse_macOS_Desktop(t *testing.T) {
	ua := "Mozilla/5.0 (Macintosh; Intel Mac OS X 14_0) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36"
	result := useragent.Parse(ua)

	if result.OSFamily != "macOS" {
		t.Errorf("expected macOS, got %q", result.OSFamily)
	}
	if result.DeviceType != "desktop" {
		t.Errorf("expected desktop for macOS, got %q", result.DeviceType)
	}
}

// ── ExtractReferrerDomain tests ───────────────────────────────────────────────

func TestExtractReferrerDomain_DirectTraffic(t *testing.T) {
	if got := useragent.ExtractReferrerDomain(""); got != "direct" {
		t.Errorf("expected 'direct' for empty referrer, got %q", got)
	}
}

func TestExtractReferrerDomain_FullURL(t *testing.T) {
	cases := []struct {
		referrer string
		expected string
	}{
		{"https://www.google.com/search?q=test", "www.google.com"},
		{"https://twitter.com/user/status/123", "twitter.com"},
		{"http://example.com/page?ref=newsletter", "example.com"},
		{"https://t.co/abc123", "t.co"},
	}
	for _, tc := range cases {
		got := useragent.ExtractReferrerDomain(tc.referrer)
		if got != tc.expected {
			t.Errorf("referrer=%q: expected %q, got %q", tc.referrer, tc.expected, got)
		}
	}
}

func TestExtractReferrerDomain_NoScheme(t *testing.T) {
	got := useragent.ExtractReferrerDomain("www.example.com/path")
	if got != "www.example.com" {
		t.Errorf("expected www.example.com, got %q", got)
	}
}

// ── Benchmark ────────────────────────────────────────────────────────────────

func BenchmarkParse_Desktop(b *testing.B) {
	ua := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		useragent.Parse(ua)
	}
}

func BenchmarkParse_Bot(b *testing.B) {
	ua := "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		useragent.Parse(ua)
	}
}
