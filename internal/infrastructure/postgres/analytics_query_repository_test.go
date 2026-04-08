//go:build integration
// +build integration

package postgres_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/urlshortener/platform/internal/domain/analytics"
	"github.com/urlshortener/platform/internal/infrastructure/postgres"
)

func testAnalyticsQueryRepo(t *testing.T) *postgres.AnalyticsQueryRepository {
	t.Helper()
	return postgres.NewAnalyticsQueryRepository(testClient(t))
}

// seedRedirectEvents inserts test events directly via SQL.
// This bypasses the analytics write path to make integration tests self-contained.
func seedRedirectEvents(t *testing.T, shortCode, workspaceID string, count int, isBot bool) {
	t.Helper()
	client := testClient(t)
	ctx := context.Background()

	for i := 0; i < count; i++ {
		_, err := client.Primary().Exec(ctx, `
			INSERT INTO redirect_events (
				id, short_code, workspace_id, occurred_at,
				ip_hash, country_code, device_type, browser_family,
				os_family, referrer_domain, is_bot
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
			ulid.Make().String(),
			shortCode,
			workspaceID,
			time.Now().UTC().Add(-time.Duration(i)*time.Minute),
			fmt.Sprintf("hash_%d", i),
			[]string{"US", "GB", "DE", "FR", "JP"}[i%5],
			[]string{"desktop", "mobile", "tablet"}[i%3],
			[]string{"Chrome", "Firefox", "Safari"}[i%3],
			"Linux",
			"github.com",
			isBot,
		)
		if err != nil {
			t.Fatalf("seedRedirectEvents: insert %d failed: %v", i, err)
		}
	}
}

func TestAnalyticsQueryRepository_GetSummary(t *testing.T) {
	repo := testAnalyticsQueryRepo(t)
	ctx := context.Background()

	code := "analytics_sum_" + ulid.Make().String()[:6]
	wsID := "ws_analytics_test"

	// 10 human clicks + 3 bot clicks
	seedRedirectEvents(t, code, wsID, 10, false)
	seedRedirectEvents(t, code, wsID, 3, true)

	start := time.Now().UTC().Add(-1 * time.Hour)
	end := time.Now().UTC().Add(1 * time.Minute)

	summary, err := repo.GetSummary(ctx, code, start, end)
	if err != nil {
		t.Fatalf("GetSummary failed: %v", err)
	}

	if summary.TotalClicks != 10 {
		t.Errorf("expected TotalClicks=10, got %d", summary.TotalClicks)
	}
	if summary.BotClicks != 3 {
		t.Errorf("expected BotClicks=3, got %d", summary.BotClicks)
	}
	if summary.UniqueIPs == 0 {
		t.Error("expected non-zero UniqueIPs")
	}
}

func TestAnalyticsQueryRepository_GetSummary_OutsideWindow_Zero(t *testing.T) {
	repo := testAnalyticsQueryRepo(t)
	ctx := context.Background()

	code := "analytics_outwin_" + ulid.Make().String()[:6]
	seedRedirectEvents(t, code, "ws_test", 5, false)

	// Query a window in the future — no events should match
	start := time.Now().UTC().Add(1 * time.Hour)
	end := time.Now().UTC().Add(2 * time.Hour)

	summary, err := repo.GetSummary(ctx, code, start, end)
	if err != nil {
		t.Fatalf("GetSummary failed: %v", err)
	}
	if summary.TotalClicks != 0 {
		t.Errorf("expected 0 clicks outside window, got %d", summary.TotalClicks)
	}
}

func TestAnalyticsQueryRepository_GetTimeSeries_HasZeroBuckets(t *testing.T) {
	repo := testAnalyticsQueryRepo(t)
	ctx := context.Background()

	code := "analytics_ts_" + ulid.Make().String()[:6]
	// Only 1 event, but we query a 3-hour window with hourly granularity
	seedRedirectEvents(t, code, "ws_test", 1, false)

	start := time.Now().UTC().Add(-3 * time.Hour)
	end := time.Now().UTC().Add(1 * time.Minute)

	ts, err := repo.GetTimeSeries(ctx, code, start, end, analytics.Granularity1Hour)
	if err != nil {
		t.Fatalf("GetTimeSeries failed: %v", err)
	}

	// 3-hour window with hourly buckets: expect at least 3 points
	if len(ts.Points) < 3 {
		t.Errorf("expected at least 3 time buckets, got %d", len(ts.Points))
	}

	// At least one bucket should have 0 clicks (zero-fill test)
	hasZero := false
	for _, p := range ts.Points {
		if p.Clicks == 0 {
			hasZero = true
			break
		}
	}
	if !hasZero {
		t.Error("expected at least one zero-count bucket in time series")
	}
}

func TestAnalyticsQueryRepository_GetBreakdown_Country(t *testing.T) {
	repo := testAnalyticsQueryRepo(t)
	ctx := context.Background()

	code := "analytics_bkd_" + ulid.Make().String()[:6]
	// seedRedirectEvents uses 5 country codes cycling: US, GB, DE, FR, JP
	seedRedirectEvents(t, code, "ws_test", 10, false)

	start := time.Now().UTC().Add(-1 * time.Hour)
	end := time.Now().UTC().Add(1 * time.Minute)

	breakdown, err := repo.GetBreakdown(ctx, code, start, end, analytics.DimensionCountry)
	if err != nil {
		t.Fatalf("GetBreakdown failed: %v", err)
	}

	if breakdown.TotalClicks != 10 {
		t.Errorf("expected TotalClicks=10, got %d", breakdown.TotalClicks)
	}
	if len(breakdown.Counts) == 0 {
		t.Error("expected at least one country in breakdown")
	}

	// Verify percentages sum to approximately 100
	var totalPct float64
	for _, c := range breakdown.Counts {
		totalPct += c.Percentage
	}
	if totalPct < 99.9 || totalPct > 100.1 {
		t.Errorf("expected percentages to sum to ~100, got %.2f", totalPct)
	}
}
