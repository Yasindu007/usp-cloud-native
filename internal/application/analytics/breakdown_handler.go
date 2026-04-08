package analytics

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"

	"github.com/urlshortener/platform/internal/application/apperrors"
	"github.com/urlshortener/platform/internal/domain/analytics"
	domainurl "github.com/urlshortener/platform/internal/domain/url"
)

// BreakdownQuery carries inputs for the GetAnalyticsBreakdown use case.
type BreakdownQuery struct {
	URLID       string
	WorkspaceID string
	Window      analytics.Window
	Dimension   analytics.Dimension
}

// BreakdownResult is the API-facing breakdown response.
type BreakdownResult struct {
	ShortCode   string
	Dimension   string
	TotalClicks int64
	WindowStart time.Time
	WindowEnd   time.Time
	Counts      []DimensionCountResult
}

// DimensionCountResult is a single row in the breakdown response.
type DimensionCountResult struct {
	Value      string
	Clicks     int64
	Percentage float64
}

// BreakdownHandler orchestrates the GetAnalyticsBreakdown use case.
type BreakdownHandler struct {
	analyticsRepo QueryRepository
	urlRepo       URLReadRepository
}

// NewBreakdownHandler creates a BreakdownHandler.
func NewBreakdownHandler(ar QueryRepository, ur URLReadRepository) *BreakdownHandler {
	return &BreakdownHandler{analyticsRepo: ar, urlRepo: ur}
}

// Handle executes the GetAnalyticsBreakdown use case.
func (h *BreakdownHandler) Handle(ctx context.Context, q BreakdownQuery) (*BreakdownResult, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "GetAnalyticsBreakdown.Handle",
		attribute.String("url.id", q.URLID),
		attribute.String("analytics.dimension", string(q.Dimension)),
	)
	defer span.End()

	if !q.Window.IsValid() {
		return nil, apperrors.NewValidationError("window must be one of: 1h, 24h, 7d, 30d, all", nil)
	}
	if !q.Dimension.IsValid() {
		return nil, apperrors.NewValidationError(
			"dimension must be one of: country, device_type, browser_family, os_family, referrer_domain",
			nil,
		)
	}

	u, err := h.urlRepo.GetByID(ctx, q.URLID, q.WorkspaceID)
	if err != nil {
		if domainurl.IsNotFound(err) {
			return nil, apperrors.ErrNotFound
		}
		return nil, fmt.Errorf("resolving url for breakdown: %w", err)
	}

	end := time.Now().UTC()
	var start time.Time
	if q.Window == analytics.WindowAllTime {
		start = u.CreatedAt
	} else {
		start = end.Add(-q.Window.Duration())
	}

	breakdown, err := h.analyticsRepo.GetBreakdown(ctx, u.ShortCode, start, end, q.Dimension)
	if err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("fetching breakdown: %w", err)
	}

	counts := make([]DimensionCountResult, 0, len(breakdown.Counts))
	for _, c := range breakdown.Counts {
		counts = append(counts, DimensionCountResult{
			Value:      c.Value,
			Clicks:     c.Clicks,
			Percentage: c.Percentage,
		})
	}

	return &BreakdownResult{
		ShortCode:   u.ShortCode,
		Dimension:   string(q.Dimension),
		TotalClicks: breakdown.TotalClicks,
		WindowStart: start,
		WindowEnd:   end,
		Counts:      counts,
	}, nil
}
