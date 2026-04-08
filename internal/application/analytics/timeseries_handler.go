package analytics

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/urlshortener/platform/internal/application/apperrors"
	"github.com/urlshortener/platform/internal/domain/analytics"
	domainurl "github.com/urlshortener/platform/internal/domain/url"
)

// TimeSeriesQuery carries inputs for the GetAnalyticsTimeSeries use case.
type TimeSeriesQuery struct {
	URLID       string
	WorkspaceID string
	// Start and End allow callers to specify explicit time bounds.
	// When zero, the handler derives them from the Window field.
	Start time.Time
	End   time.Time
	// Window provides a named window when Start/End are zero.
	Window      analytics.Window
	Granularity analytics.Granularity
}

// TimeSeriesResult is the API-facing time-series response.
type TimeSeriesResult struct {
	ShortCode   string
	Granularity string
	WindowStart time.Time
	WindowEnd   time.Time
	Points      []TimeSeriesPointResult
}

// TimeSeriesPointResult is a single data point for the API response.
type TimeSeriesPointResult struct {
	BucketStart time.Time
	Clicks      int64
	UniqueIPs   int64
}

// TimeSeriesHandler orchestrates the GetAnalyticsTimeSeries use case.
type TimeSeriesHandler struct {
	analyticsRepo QueryRepository
	urlRepo       URLReadRepository
}

// NewTimeSeriesHandler creates a TimeSeriesHandler.
func NewTimeSeriesHandler(ar QueryRepository, ur URLReadRepository) *TimeSeriesHandler {
	return &TimeSeriesHandler{analyticsRepo: ar, urlRepo: ur}
}

// Handle executes the GetAnalyticsTimeSeries use case.
func (h *TimeSeriesHandler) Handle(ctx context.Context, q TimeSeriesQuery) (*TimeSeriesResult, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "GetAnalyticsTimeSeries.Handle",
		trace.WithAttributes(
			attribute.String("url.id", q.URLID),
			attribute.String("analytics.granularity", string(q.Granularity)),
		),
	)
	defer span.End()

	if !q.Granularity.IsValid() {
		return nil, apperrors.NewValidationError(
			"granularity must be one of: 1m, 1h, 1d", nil)
	}

	u, err := h.urlRepo.GetByID(ctx, q.URLID, q.WorkspaceID)
	if err != nil {
		if domainurl.IsNotFound(err) {
			return nil, apperrors.ErrNotFound
		}
		return nil, fmt.Errorf("resolving url for timeseries: %w", err)
	}

	// Resolve time window
	end := q.End
	start := q.Start
	if end.IsZero() {
		end = time.Now().UTC()
	}
	if start.IsZero() {
		if q.Window.IsValid() && q.Window != analytics.WindowAllTime {
			start = end.Add(-q.Window.Duration())
		} else {
			start = u.CreatedAt
		}
	}

	if err := analytics.ValidateWindowGranularity(start, end, q.Granularity); err != nil {
		return nil, apperrors.NewValidationError(err.Error(), err)
	}

	ts, err := h.analyticsRepo.GetTimeSeries(ctx, u.ShortCode, start, end, q.Granularity)
	if err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("fetching time series: %w", err)
	}

	points := make([]TimeSeriesPointResult, 0, len(ts.Points))
	for _, p := range ts.Points {
		points = append(points, TimeSeriesPointResult{
			BucketStart: p.BucketStart,
			Clicks:      p.Clicks,
			UniqueIPs:   p.UniqueIPs,
		})
	}

	return &TimeSeriesResult{
		ShortCode:   u.ShortCode,
		Granularity: string(q.Granularity),
		WindowStart: start,
		WindowEnd:   end,
		Points:      points,
	}, nil
}
