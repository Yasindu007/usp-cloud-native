//go:build integration
// +build integration

package postgres_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/urlshortener/platform/internal/domain/analytics"
	"github.com/urlshortener/platform/internal/infrastructure/postgres"
)

func testAnalyticsRepo(t *testing.T) *postgres.AnalyticsRepository {
	t.Helper()
	return postgres.NewAnalyticsRepository(testClient(t))
}

func newTestRedirectEvent(shortCode, workspaceID string) *analytics.RedirectEvent {
	return &analytics.RedirectEvent{
		ID:             ulid.Make().String(),
		ShortCode:      shortCode,
		WorkspaceID:    workspaceID,
		OccurredAt:     time.Now().UTC().Truncate(time.Microsecond),
		IPHash:         strings.Repeat("a", 64), // 64-char mock hash
		UserAgent:      "Mozilla/5.0 (Windows NT 10.0) Chrome/120",
		DeviceType:     analytics.DeviceTypeDesktop,
		BrowserFamily:  "Chrome",
		OSFamily:       "Windows",
		IsBot:          false,
		CountryCode:    "XX",
		ReferrerDomain: "direct",
		ReferrerRaw:    "",
		RequestID:      ulid.Make().String(),
	}
}

func TestAnalyticsRepository_WriteMany_Success(t *testing.T) {
	repo := testAnalyticsRepo(t)
	ctx := context.Background()

	events := []*analytics.RedirectEvent{
		newTestRedirectEvent("abc1234", "ws_test"),
		newTestRedirectEvent("abc1234", "ws_test"),
		newTestRedirectEvent("xyz9876", "ws_test"),
	}

	if err := repo.WriteMany(ctx, events); err != nil {
		t.Fatalf("WriteMany failed: %v", err)
	}
}

func TestAnalyticsRepository_WriteMany_Empty_NoError(t *testing.T) {
	repo := testAnalyticsRepo(t)
	ctx := context.Background()

	if err := repo.WriteMany(ctx, nil); err != nil {
		t.Fatalf("WriteMany(nil) should not error: %v", err)
	}
	if err := repo.WriteMany(ctx, []*analytics.RedirectEvent{}); err != nil {
		t.Fatalf("WriteMany([]) should not error: %v", err)
	}
}

func TestAnalyticsRepository_WriteMany_BotEvent_NilIPHash(t *testing.T) {
	repo := testAnalyticsRepo(t)
	ctx := context.Background()

	botEvent := newTestRedirectEvent("abc1234", "ws_test")
	botEvent.IsBot = true
	botEvent.IPHash = "" // bots don't get IP hashed
	botEvent.DeviceType = analytics.DeviceTypeBot
	botEvent.BrowserFamily = "bot"

	if err := repo.WriteMany(ctx, []*analytics.RedirectEvent{botEvent}); err != nil {
		t.Fatalf("WriteMany with bot event failed: %v", err)
	}
}

func TestAnalyticsRepository_WriteMany_Idempotent_OnDuplicateID(t *testing.T) {
	repo := testAnalyticsRepo(t)
	ctx := context.Background()

	evt := newTestRedirectEvent("abc1234", "ws_test")

	// First insert
	if err := repo.WriteMany(ctx, []*analytics.RedirectEvent{evt}); err != nil {
		t.Fatalf("first WriteMany failed: %v", err)
	}
	// Second insert with same ID → ON CONFLICT DO NOTHING
	if err := repo.WriteMany(ctx, []*analytics.RedirectEvent{evt}); err != nil {
		t.Fatalf("duplicate WriteMany should not error (ON CONFLICT DO NOTHING): %v", err)
	}
}

func TestAnalyticsRepository_IncrementClickCounts_Success(t *testing.T) {
	repo := testAnalyticsRepo(t)
	wsRepo := postgres.NewWorkspaceRepository(testClient(t))
	urlRepo := postgres.NewURLRepository(testClient(t))
	ctx := context.Background()

	// Create workspace + URL for the click count test
	ownerID := ulid.Make().String()
	ws := createTestWorkspaceForAPIKeys(t) // reuse helper from apikey test
	_ = ws

	// We need an actual URL row for the click_count update to affect
	// Use the ws_default seeded workspace
	wsID := "ws_default"
	_ = wsRepo

	// Create a URL in ws_default
	u := newTestURL(wsID)
	if err := urlRepo.Create(ctx, u); err != nil {
		t.Fatalf("setup: create test URL failed: %v", err)
	}

	_ = ownerID

	counts := map[string]int64{
		u.ShortCode: 5,
	}
	if err := repo.IncrementClickCounts(ctx, counts); err != nil {
		t.Fatalf("IncrementClickCounts failed: %v", err)
	}

	// Verify click_count was incremented
	updated, err := urlRepo.GetByShortCode(ctx, u.ShortCode)
	if err != nil {
		t.Fatalf("GetByShortCode failed: %v", err)
	}
	if updated.ClickCount != 5 {
		t.Errorf("expected click_count=5 after increment, got %d", updated.ClickCount)
	}
}

func TestAnalyticsRepository_IncrementClickCounts_Empty_NoError(t *testing.T) {
	repo := testAnalyticsRepo(t)
	ctx := context.Background()

	if err := repo.IncrementClickCounts(ctx, map[string]int64{}); err != nil {
		t.Fatalf("IncrementClickCounts(empty) should not error: %v", err)
	}
}
