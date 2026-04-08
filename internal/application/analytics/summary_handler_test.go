package analytics_test

import (
	"context"
	"errors"
	"testing"
	"time"

	appanalytics "github.com/urlshortener/platform/internal/application/analytics"
	"github.com/urlshortener/platform/internal/application/apperrors"
	"github.com/urlshortener/platform/internal/domain/analytics"
	domainurl "github.com/urlshortener/platform/internal/domain/url"
)

// ── Shared fakes ──────────────────────────────────────────────────────────────

type fakeQueryRepo struct {
	summary    *analytics.Summary
	timeSeries *analytics.TimeSeries
	breakdown  *analytics.Breakdown
	err        error
}

func (r *fakeQueryRepo) GetSummary(_ context.Context, _ string, _, _ time.Time) (*analytics.Summary, error) {
	return r.summary, r.err
}
func (r *fakeQueryRepo) GetTimeSeries(_ context.Context, _ string, _, _ time.Time, _ analytics.Granularity) (*analytics.TimeSeries, error) {
	return r.timeSeries, r.err
}
func (r *fakeQueryRepo) GetBreakdown(_ context.Context, _ string, _, _ time.Time, _ analytics.Dimension) (*analytics.Breakdown, error) {
	return r.breakdown, r.err
}

type fakeURLReadRepo struct {
	url *domainurl.URL
	err error
}

func (r *fakeURLReadRepo) GetByID(_ context.Context, _, _ string) (*domainurl.URL, error) {
	return r.url, r.err
}

func newTestURL(shortCode string) *domainurl.URL {
	return &domainurl.URL{
		ID:          "url_test_001",
		WorkspaceID: "ws_001",
		ShortCode:   shortCode,
		OriginalURL: "https://example.com",
		Status:      domainurl.StatusActive,
		CreatedAt:   time.Now().UTC().Add(-30 * 24 * time.Hour),
	}
}

// ── SummaryHandler tests ──────────────────────────────────────────────────────

func TestSummaryHandler_Handle_Success(t *testing.T) {
	urlRepo := &fakeURLReadRepo{url: newTestURL("abc1234")}
	analyticsRepo := &fakeQueryRepo{
		summary: &analytics.Summary{
			ShortCode:   "abc1234",
			TotalClicks: 142,
			UniqueIPs:   87,
			BotClicks:   12,
		},
	}
	h := appanalytics.NewSummaryHandler(analyticsRepo, urlRepo, "https://s.example.com")

	result, err := h.Handle(context.Background(), appanalytics.SummaryQuery{
		URLID:       "url_test_001",
		WorkspaceID: "ws_001",
		Window:      analytics.Window24Hour,
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.TotalClicks != 142 {
		t.Errorf("expected TotalClicks=142, got %d", result.TotalClicks)
	}
	if result.UniqueIPs != 87 {
		t.Errorf("expected UniqueIPs=87, got %d", result.UniqueIPs)
	}
	if result.BotClicks != 12 {
		t.Errorf("expected BotClicks=12, got %d", result.BotClicks)
	}
	if result.ShortCode != "abc1234" {
		t.Errorf("expected ShortCode=abc1234, got %q", result.ShortCode)
	}
	if result.ShortURL != "https://s.example.com/abc1234" {
		t.Errorf("unexpected ShortURL: %q", result.ShortURL)
	}
}

func TestSummaryHandler_Handle_InvalidWindow(t *testing.T) {
	h := appanalytics.NewSummaryHandler(&fakeQueryRepo{}, &fakeURLReadRepo{}, "")

	_, err := h.Handle(context.Background(), appanalytics.SummaryQuery{
		URLID:       "url_001",
		WorkspaceID: "ws_001",
		Window:      analytics.Window("invalid"),
	})

	if !apperrors.IsValidationError(err) {
		t.Errorf("expected validation error for invalid window, got: %v", err)
	}
}

func TestSummaryHandler_Handle_URLNotFound(t *testing.T) {
	urlRepo := &fakeURLReadRepo{err: domainurl.ErrNotFound}
	h := appanalytics.NewSummaryHandler(&fakeQueryRepo{}, urlRepo, "")

	_, err := h.Handle(context.Background(), appanalytics.SummaryQuery{
		URLID:       "ghost",
		WorkspaceID: "ws_001",
		Window:      analytics.Window24Hour,
	})

	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got: %v", err)
	}
}

func TestSummaryHandler_Handle_QueryError(t *testing.T) {
	urlRepo := &fakeURLReadRepo{url: newTestURL("abc1234")}
	analyticsRepo := &fakeQueryRepo{err: errors.New("db: connection refused")}
	h := appanalytics.NewSummaryHandler(analyticsRepo, urlRepo, "")

	_, err := h.Handle(context.Background(), appanalytics.SummaryQuery{
		URLID:       "url_001",
		WorkspaceID: "ws_001",
		Window:      analytics.Window24Hour,
	})

	if err == nil {
		t.Error("expected error when analytics DB fails")
	}
}

func TestSummaryHandler_Handle_AllTime_UsesURLCreatedAt(t *testing.T) {
	urlRepo := &fakeURLReadRepo{url: newTestURL("abc1234")}
	analyticsRepo := &fakeQueryRepo{
		summary: &analytics.Summary{ShortCode: "abc1234", TotalClicks: 500},
	}
	h := appanalytics.NewSummaryHandler(analyticsRepo, urlRepo, "")

	result, err := h.Handle(context.Background(), appanalytics.SummaryQuery{
		URLID:       "url_001",
		WorkspaceID: "ws_001",
		Window:      analytics.WindowAllTime,
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// WindowStart should be approximately the URL creation time (30 days ago)
	expectedStart := newTestURL("").CreatedAt
	diff := result.WindowStart.Sub(expectedStart)
	if diff > time.Second || diff < -time.Second {
		t.Errorf("expected WindowStart near URL creation, got %v", result.WindowStart)
	}
}
