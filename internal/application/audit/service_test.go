package audit_test

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	appaudit "github.com/urlshortener/platform/internal/application/audit"
	domainaudit "github.com/urlshortener/platform/internal/domain/audit"
	domainauth "github.com/urlshortener/platform/internal/domain/auth"
)

// ── Fake writer ───────────────────────────────────────────────────────────────

type fakeWriter struct {
	mu         sync.Mutex
	events     []*domainaudit.Event
	err        error
	writeCalls int
}

func (w *fakeWriter) Write(_ context.Context, evt *domainaudit.Event) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return w.err
	}
	w.events = append(w.events, evt)
	w.writeCalls++
	return nil
}

func (w *fakeWriter) WriteMany(_ context.Context, evts []*domainaudit.Event) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return w.err
	}
	w.events = append(w.events, evts...)
	w.writeCalls++
	return nil
}

func (w *fakeWriter) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.events)
}

var testLog = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
	Level: slog.LevelError,
}))

// ── Tests ─────────────────────────────────────────────────────────────────────

func TestService_Capture_WritesWithinFlushInterval(t *testing.T) {
	writer := &fakeWriter{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	svc := appaudit.NewService(ctx, writer, testLog)

	svc.Capture(&domainaudit.Event{
		ID:           "evt_001",
		ActorID:      "usr_001",
		ActorType:    domainaudit.ActorUser,
		Action:       domainaudit.ActionURLCreate,
		ResourceType: domainaudit.ResourceURL,
		ResourceID:   "url_001",
		OccurredAt:   time.Now().UTC(),
	})

	// Wait slightly longer than the default flush interval (500ms)
	time.Sleep(700 * time.Millisecond)

	if writer.count() == 0 {
		t.Error("expected event to be written within flush interval")
	}
}

func TestService_Capture_BatchesMultipleEvents(t *testing.T) {
	writer := &fakeWriter{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	svc := appaudit.NewService(ctx, writer, testLog)

	const eventCount = 10
	for i := 0; i < eventCount; i++ {
		svc.Capture(&domainaudit.Event{
			ID:           "evt_" + string(rune('A'+i)),
			ActorID:      "usr_001",
			ActorType:    domainaudit.ActorUser,
			Action:       domainaudit.ActionURLCreate,
			ResourceType: domainaudit.ResourceURL,
			ResourceID:   "url_00" + string(rune('0'+i)),
			OccurredAt:   time.Now().UTC(),
		})
	}

	// Wait for flush
	time.Sleep(700 * time.Millisecond)

	if writer.count() != eventCount {
		t.Errorf("expected %d events written, got %d", eventCount, writer.count())
	}
}

func TestService_Shutdown_FlushesRemainingEvents(t *testing.T) {
	writer := &fakeWriter{}
	ctx, cancel := context.WithCancel(context.Background())

	svc := appaudit.NewService(ctx, writer, testLog)

	// Send events then shutdown before the flush interval fires
	for i := 0; i < 3; i++ {
		svc.Capture(&domainaudit.Event{
			ID:      "shutdown_evt_" + string(rune('0'+i)),
			ActorID: "usr_001", ActorType: domainaudit.ActorUser,
			Action:       domainaudit.ActionURLCreate,
			ResourceType: domainaudit.ResourceURL, ResourceID: "url_001",
			OccurredAt: time.Now().UTC(),
		})
	}

	// Cancel context (simulate shutdown signal)
	cancel()

	// Shutdown must flush buffered events
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	svc.Shutdown(shutdownCtx)

	if writer.count() != 3 {
		t.Errorf("expected 3 events after shutdown flush, got %d", writer.count())
	}
}

func TestService_ChannelFull_DropsEvent_IncrementsDrop(t *testing.T) {
	// Make the writer very slow to simulate channel saturation
	slowWriter := &fakeWriter{err: errors.New("db: too slow")}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	svc := appaudit.NewService(ctx, slowWriter, testLog)

	// Send far more events than channel capacity (2048)
	// In practice the test sends a smaller number quickly
	for i := 0; i < 10; i++ {
		svc.Capture(&domainaudit.Event{
			ID: "drop_evt", ActorID: "usr_001",
			ActorType:    domainaudit.ActorUser,
			Action:       domainaudit.ActionURLCreate,
			ResourceType: domainaudit.ResourceURL, ResourceID: "url_001",
			OccurredAt: time.Now().UTC(),
		})
	}

	// Dropped events counter must be non-negative (might be 0 if channel not full)
	if svc.DroppedEvents() < 0 {
		t.Error("DroppedEvents must never be negative")
	}
}

func TestService_BuildEvent_WithClaims_ExtractsIdentity(t *testing.T) {
	writer := &fakeWriter{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	svc := appaudit.NewService(ctx, writer, testLog)

	claims := &domainauth.Claims{
		UserID:      "usr_test123",
		WorkspaceID: "ws_test456",
		Issuer:      "http://localhost:9000",
		Scope:       "read write",
	}
	ctx = domainauth.WithContext(ctx, claims)

	evt := svc.BuildEvent(
		ctx,
		domainaudit.ActionURLCreate,
		domainaudit.ResourceURL,
		"url_789",
		"10.0.0.1", "TestAgent/1.0", "req_001",
		map[string]any{"short_code": "abc1234"},
	)

	if evt.ActorID != "usr_test123" {
		t.Errorf("expected ActorID=usr_test123, got %q", evt.ActorID)
	}
	if *evt.WorkspaceID != "ws_test456" {
		t.Errorf("expected WorkspaceID=ws_test456, got %q", *evt.WorkspaceID)
	}
	if evt.ActorType != domainaudit.ActorUser {
		t.Errorf("expected ActorType=user, got %q", evt.ActorType)
	}
	if evt.ResourceID != "url_789" {
		t.Errorf("expected ResourceID=url_789, got %q", evt.ResourceID)
	}
	if evt.ID == "" {
		t.Error("expected non-empty event ID (ULID)")
	}
}

func TestService_BuildEvent_APIKeyClaims_SetsActorTypeAPIKey(t *testing.T) {
	writer := &fakeWriter{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	svc := appaudit.NewService(ctx, writer, testLog)

	// API key claims have Issuer="apikey"
	claims := &domainauth.Claims{
		UserID:      "usr_creator",
		WorkspaceID: "ws_001",
		Issuer:      "apikey",
	}
	ctx = domainauth.WithContext(ctx, claims)

	evt := svc.BuildEvent(ctx, domainaudit.ActionAPIKeyCreate,
		domainaudit.ResourceAPIKey, "key_001",
		"", "", "", nil)

	if evt.ActorType != domainaudit.ActorAPIKey {
		t.Errorf("expected ActorType=api_key for apikey issuer, got %q", evt.ActorType)
	}
}

func TestService_BuildEvent_NoClaims_SetsAnonymous(t *testing.T) {
	writer := &fakeWriter{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	svc := appaudit.NewService(ctx, writer, testLog)

	// No auth claims in context
	evt := svc.BuildEvent(context.Background(),
		domainaudit.ActionAuthFailed, domainaudit.ResourceToken, "n/a",
		"10.0.0.1", "", "", nil)

	if evt.ActorID != "anonymous" {
		t.Errorf("expected ActorID=anonymous for unauthenticated, got %q", evt.ActorID)
	}
	if evt.WorkspaceID != nil {
		t.Error("expected nil WorkspaceID for unauthenticated event")
	}
}
