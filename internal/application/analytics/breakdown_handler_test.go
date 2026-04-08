package analytics_test

import (
	"context"
	"testing"

	appanalytics "github.com/urlshortener/platform/internal/application/analytics"
	"github.com/urlshortener/platform/internal/application/apperrors"
	"github.com/urlshortener/platform/internal/domain/analytics"
	domainurl "github.com/urlshortener/platform/internal/domain/url"
)

func TestBreakdownHandler_Handle_Success(t *testing.T) {
	urlRepo := &fakeURLReadRepo{url: newTestURL("abc1234")}
	analyticsRepo := &fakeQueryRepo{
		breakdown: &analytics.Breakdown{
			ShortCode:   "abc1234",
			Dimension:   analytics.DimensionCountry,
			TotalClicks: 100,
			Counts: []analytics.DimensionCount{
				{Value: "US", Clicks: 60, Percentage: 60.0},
				{Value: "GB", Clicks: 25, Percentage: 25.0},
				{Value: "DE", Clicks: 15, Percentage: 15.0},
			},
		},
	}
	h := appanalytics.NewBreakdownHandler(analyticsRepo, urlRepo)

	result, err := h.Handle(context.Background(), appanalytics.BreakdownQuery{
		URLID:       "url_001",
		WorkspaceID: "ws_001",
		Window:      analytics.Window7Day,
		Dimension:   analytics.DimensionCountry,
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.TotalClicks != 100 {
		t.Errorf("expected TotalClicks=100, got %d", result.TotalClicks)
	}
	if len(result.Counts) != 3 {
		t.Errorf("expected 3 countries, got %d", len(result.Counts))
	}
	if result.Counts[0].Value != "US" {
		t.Errorf("expected top country=US, got %q", result.Counts[0].Value)
	}
}

func TestBreakdownHandler_Handle_InvalidDimension(t *testing.T) {
	h := appanalytics.NewBreakdownHandler(&fakeQueryRepo{}, &fakeURLReadRepo{})

	_, err := h.Handle(context.Background(), appanalytics.BreakdownQuery{
		URLID:       "url_001",
		WorkspaceID: "ws_001",
		Window:      analytics.Window24Hour,
		Dimension:   analytics.Dimension("invalid_field"),
	})

	if !apperrors.IsValidationError(err) {
		t.Errorf("expected validation error for invalid dimension, got: %v", err)
	}
}

func TestBreakdownHandler_Handle_InvalidWindow(t *testing.T) {
	h := appanalytics.NewBreakdownHandler(&fakeQueryRepo{}, &fakeURLReadRepo{})

	_, err := h.Handle(context.Background(), appanalytics.BreakdownQuery{
		URLID:       "url_001",
		WorkspaceID: "ws_001",
		Window:      analytics.Window("bad"),
		Dimension:   analytics.DimensionCountry,
	})

	if !apperrors.IsValidationError(err) {
		t.Errorf("expected validation error for invalid window, got: %v", err)
	}
}

func TestBreakdownHandler_Handle_URLNotFound(t *testing.T) {
	urlRepo := &fakeURLReadRepo{err: domainurl.ErrNotFound}
	h := appanalytics.NewBreakdownHandler(&fakeQueryRepo{}, urlRepo)

	_, err := h.Handle(context.Background(), appanalytics.BreakdownQuery{
		URLID:       "ghost",
		WorkspaceID: "ws_001",
		Window:      analytics.Window24Hour,
		Dimension:   analytics.DimensionCountry,
	})

	if !apperrors.IsNotFound(err) {
		t.Errorf("expected ErrNotFound, got: %v", err)
	}
}
