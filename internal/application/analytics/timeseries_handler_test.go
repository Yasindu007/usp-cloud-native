package analytics_test

import (
	"context"
	"testing"
	"time"

	appanalytics "github.com/urlshortener/platform/internal/application/analytics"
	"github.com/urlshortener/platform/internal/application/apperrors"
	"github.com/urlshortener/platform/internal/domain/analytics"
)

func TestTimeSeriesHandler_Handle_Success(t *testing.T) {
	urlRepo := &fakeURLReadRepo{url: newTestURL("abc1234")}
	analyticsRepo := &fakeQueryRepo{
		timeSeries: &analytics.TimeSeries{
			ShortCode:   "abc1234",
			Granularity: analytics.Granularity1Hour,
			Points: []analytics.TimeSeriesPoint{
				{BucketStart: time.Now().Add(-2 * time.Hour), Clicks: 10, UniqueIPs: 8},
				{BucketStart: time.Now().Add(-1 * time.Hour), Clicks: 25, UniqueIPs: 20},
				{BucketStart: time.Now(), Clicks: 5, UniqueIPs: 4},
			},
		},
	}
	h := appanalytics.NewTimeSeriesHandler(analyticsRepo, urlRepo)

	result, err := h.Handle(context.Background(), appanalytics.TimeSeriesQuery{
		URLID:       "url_001",
		WorkspaceID: "ws_001",
		Window:      analytics.Window24Hour,
		Granularity: analytics.Granularity1Hour,
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Points) != 3 {
		t.Errorf("expected 3 points, got %d", len(result.Points))
	}
	if result.Points[1].Clicks != 25 {
		t.Errorf("expected second point Clicks=25, got %d", result.Points[1].Clicks)
	}
	if result.Granularity != "1h" {
		t.Errorf("expected Granularity=1h, got %q", result.Granularity)
	}
}

func TestTimeSeriesHandler_Handle_InvalidGranularity(t *testing.T) {
	h := appanalytics.NewTimeSeriesHandler(&fakeQueryRepo{}, &fakeURLReadRepo{})

	_, err := h.Handle(context.Background(), appanalytics.TimeSeriesQuery{
		URLID:       "url_001",
		WorkspaceID: "ws_001",
		Granularity: analytics.Granularity("5m"), // not a valid granularity
	})

	if !apperrors.IsValidationError(err) {
		t.Errorf("expected validation error for invalid granularity, got: %v", err)
	}
}

func TestTimeSeriesHandler_Handle_WindowTooWideForGranularity(t *testing.T) {
	urlRepo := &fakeURLReadRepo{url: newTestURL("abc1234")}
	h := appanalytics.NewTimeSeriesHandler(&fakeQueryRepo{}, urlRepo)

	// 7-day window with 1-minute granularity exceeds the 24h limit
	_, err := h.Handle(context.Background(), appanalytics.TimeSeriesQuery{
		URLID:       "url_001",
		WorkspaceID: "ws_001",
		Window:      analytics.Window7Day,
		Granularity: analytics.Granularity1Minute,
	})

	if !apperrors.IsValidationError(err) {
		t.Errorf("expected validation error for incompatible window+granularity, got: %v", err)
	}
}

func TestTimeSeriesHandler_Handle_ExplicitStartEnd(t *testing.T) {
	urlRepo := &fakeURLReadRepo{url: newTestURL("abc1234")}
	analyticsRepo := &fakeQueryRepo{
		timeSeries: &analytics.TimeSeries{
			ShortCode: "abc1234",
			Points:    []analytics.TimeSeriesPoint{},
		},
	}
	h := appanalytics.NewTimeSeriesHandler(analyticsRepo, urlRepo)

	start := time.Now().UTC().Add(-12 * time.Hour)
	end := time.Now().UTC()

	result, err := h.Handle(context.Background(), appanalytics.TimeSeriesQuery{
		URLID:       "url_001",
		WorkspaceID: "ws_001",
		Start:       start,
		End:         end,
		Granularity: analytics.Granularity1Hour,
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.WindowStart.Equal(start) {
		t.Errorf("expected WindowStart=%v, got %v", start, result.WindowStart)
	}
}
