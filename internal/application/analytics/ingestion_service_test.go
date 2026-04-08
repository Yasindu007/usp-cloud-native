package analytics_test

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	appanalytics "github.com/urlshortener/platform/internal/application/analytics"
	"github.com/urlshortener/platform/internal/domain/analytics"
	"github.com/urlshortener/platform/pkg/iphasher"
)

// ── Fake writer ────────────────────────────────────────────────────────────────

type fakeAnalyticsWriter struct {
	mu           sync.Mutex
	events       []*analytics.RedirectEvent
	clickCounts  map[string]int64
	writeErr     error
	incrementErr error
	writeCalls   int
}

func newFakeWriter() *fakeAnalyticsWriter {
	return &fakeAnalyticsWriter{clickCounts: make(map[string]int64)}
}

func (w *fakeAnalyticsWriter) WriteMany(_ context.Context, events []*analytics.RedirectEvent) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.writeErr != nil {
		return w.writeErr
	}
	w.events = append(w.events, events...)
	w.writeCalls++
	return nil
}

func (w *fakeAnalyticsWriter) IncrementClickCounts(_ context.Context, counts map[string]int64) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.incrementErr != nil {
		return w.incrementErr
	}
	for code, count := range counts {
		w.clickCounts[code] += count
	}
	return nil
}

func (w *fakeAnalyticsWriter) eventCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.events)
}

func (w *fakeAnalyticsWriter) clickCount(code string) int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.clickCounts[code]
}

var testLog = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
	Level: slog.LevelError,
}))

func newTestService(writer appanalytics.Writer) *appanalytics.Service {
	ctx := context.Background() // test services are manually shut down
	hasher := iphasher.New("test-secret-key")
	return appanalytics.NewService(ctx, writer, hasher, testLog)
}

func testCaptureRequest(shortCode, workspaceID string) appanalytics.CaptureRequest {
	return appanalytics.CaptureRequest{
		ShortCode:    shortCode,
		WorkspaceID:  workspaceID,
		RawIP:        "203.0.113.42",
		UserAgentRaw: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/120.0.0.0",
		ReferrerRaw:  "https://www.google.com/search?q=test",
		RequestID:    "req_test_001",
		OccurredAt:   time.Now().UTC(),
	}
}

// ── Tests ─────────────────────────────────────────────────────────────────────

func TestService_Capture_WritesWithinFlushInterval(t *testing.T) {
	writer := newFakeWriter()
	svc := newTestService(writer)

	svc.Capture(testCaptureRequest("abc1234", "ws_001"))

	// Wait slightly longer than flush interval (100ms)
	time.Sleep(200 * time.Millisecond)

	if writer.eventCount() == 0 {
		t.Error("expected event to be written within flush interval")
	}
}

func TestService_Capture_EnrichesEvent_BotDetection(t *testing.T) {
	writer := newFakeWriter()
	svc := newTestService(writer)

	svc.Capture(appanalytics.CaptureRequest{
		ShortCode:    "abc1234",
		WorkspaceID:  "ws_001",
		RawIP:        "66.249.66.1",
		UserAgentRaw: "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)",
		OccurredAt:   time.Now().UTC(),
	})

	time.Sleep(200 * time.Millisecond)

	writer.mu.Lock()
	defer writer.mu.Unlock()

	if len(writer.events) == 0 {
		t.Fatal("expected at least one event")
	}
	evt := writer.events[0]
	if !evt.IsBot {
		t.Error("Googlebot UA must produce IsBot=true")
	}
	if evt.IPHash != "" {
		t.Error("bot events must not have an IP hash")
	}
	if evt.DeviceType != analytics.DeviceTypeBot {
		t.Errorf("expected device_type=bot, got %q", evt.DeviceType)
	}
}

func TestService_Capture_EnrichesEvent_DeviceType(t *testing.T) {
	writer := newFakeWriter()
	svc := newTestService(writer)

	svc.Capture(appanalytics.CaptureRequest{
		ShortCode:    "abc1234",
		WorkspaceID:  "ws_001",
		RawIP:        "203.0.113.1",
		UserAgentRaw: "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0) Safari/604.1",
		OccurredAt:   time.Now().UTC(),
	})

	time.Sleep(200 * time.Millisecond)

	writer.mu.Lock()
	defer writer.mu.Unlock()

	if len(writer.events) == 0 {
		t.Fatal("expected at least one event")
	}
	evt := writer.events[0]
	if evt.DeviceType != analytics.DeviceTypeMobile {
		t.Errorf("expected mobile, got %q", evt.DeviceType)
	}
	if evt.IsBot {
		t.Error("iPhone UA must not be classified as bot")
	}
	if evt.IPHash == "" {
		t.Error("non-bot event must have an IP hash")
	}
}

func TestService_Capture_IPHashIsPseudonymised(t *testing.T) {
	writer := newFakeWriter()
	svc := newTestService(writer)

	rawIP := "203.0.113.42"
	svc.Capture(appanalytics.CaptureRequest{
		ShortCode:    "abc1234",
		WorkspaceID:  "ws_001",
		RawIP:        rawIP,
		UserAgentRaw: "Mozilla/5.0 (Windows NT 10.0) Chrome/120",
		OccurredAt:   time.Now().UTC(),
	})

	time.Sleep(200 * time.Millisecond)

	writer.mu.Lock()
	defer writer.mu.Unlock()

	if len(writer.events) == 0 {
		t.Fatal("expected at least one event")
	}
	evt := writer.events[0]
	// IP hash must not contain the raw IP
	if evt.IPHash == rawIP {
		t.Error("raw IP must never be stored as ip_hash")
	}
	// Hash must be 64 chars (SHA-256 hex)
	if len(evt.IPHash) != 64 {
		t.Errorf("expected 64-char ip_hash, got %d chars", len(evt.IPHash))
	}
}

func TestService_Capture_ReferrerExtracted(t *testing.T) {
	writer := newFakeWriter()
	svc := newTestService(writer)

	svc.Capture(appanalytics.CaptureRequest{
		ShortCode:    "abc1234",
		WorkspaceID:  "ws_001",
		RawIP:        "203.0.113.1",
		UserAgentRaw: "Mozilla/5.0 Chrome/120",
		ReferrerRaw:  "https://www.twitter.com/user/status/123?utm_source=share",
		OccurredAt:   time.Now().UTC(),
	})

	time.Sleep(200 * time.Millisecond)

	writer.mu.Lock()
	defer writer.mu.Unlock()

	if len(writer.events) == 0 {
		t.Fatal("expected at least one event")
	}
	evt := writer.events[0]
	if evt.ReferrerDomain != "www.twitter.com" {
		t.Errorf("expected ReferrerDomain=www.twitter.com, got %q", evt.ReferrerDomain)
	}
}

func TestService_Capture_ClickCountIncrementedForNonBots(t *testing.T) {
	writer := newFakeWriter()
	svc := newTestService(writer)

	// 3 human redirects + 1 bot redirect for same short code
	for i := 0; i < 3; i++ {
		svc.Capture(testCaptureRequest("abc1234", "ws_001"))
	}
	svc.Capture(appanalytics.CaptureRequest{
		ShortCode:    "abc1234",
		WorkspaceID:  "ws_001",
		UserAgentRaw: "Googlebot/2.1",
		OccurredAt:   time.Now().UTC(),
	})

	time.Sleep(200 * time.Millisecond)

	// click_count should only count non-bot redirects (3, not 4)
	if count := writer.clickCount("abc1234"); count != 3 {
		t.Errorf("expected click_count=3 (bots excluded), got %d", count)
	}
}

func TestService_Capture_NonBlocking_ChannelFull(t *testing.T) {
	// A very slow writer to simulate channel saturation
	slowWriter := newFakeWriter()
	// Note: in real saturation the channel fills, not the writer is slow.
	// This test verifies Capture() returns immediately even when channel is full.
	svc := newTestService(slowWriter)

	// Send many events very quickly — should never block
	start := time.Now()
	for i := 0; i < 100; i++ {
		svc.Capture(testCaptureRequest("abc1234", "ws_001"))
	}
	elapsed := time.Since(start)

	// 100 non-blocking sends should complete in well under 10ms
	if elapsed > 100*time.Millisecond {
		t.Errorf("Capture must not block: 100 sends took %v", elapsed)
	}
}

func TestService_Shutdown_FlushesRemainingEvents(t *testing.T) {
	writer := newFakeWriter()
	ctx, cancel := context.WithCancel(context.Background())
	hasher := iphasher.New("test-secret")
	svc := appanalytics.NewService(ctx, writer, hasher, testLog)

	// Send events then immediately cancel to trigger shutdown before flush
	for i := 0; i < 5; i++ {
		svc.Capture(testCaptureRequest("abc1234", "ws_001"))
	}

	// Simulate graceful shutdown
	cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	svc.Shutdown(shutdownCtx)

	if writer.eventCount() != 5 {
		t.Errorf("expected 5 events after shutdown flush, got %d", writer.eventCount())
	}
}

func TestService_DroppedEvents_InitiallyZero(t *testing.T) {
	writer := newFakeWriter()
	svc := newTestService(writer)

	if svc.DroppedEvents() != 0 {
		t.Error("expected DroppedEvents=0 at startup")
	}
}

func TestService_WrittenEvents_TracksSuccessfulWrites(t *testing.T) {
	writer := newFakeWriter()
	svc := newTestService(writer)

	svc.Capture(testCaptureRequest("abc1234", "ws_001"))
	svc.Capture(testCaptureRequest("xyz9876", "ws_001"))

	time.Sleep(200 * time.Millisecond)

	if svc.WrittenEvents() != 2 {
		t.Errorf("expected WrittenEvents=2, got %d", svc.WrittenEvents())
	}
}
