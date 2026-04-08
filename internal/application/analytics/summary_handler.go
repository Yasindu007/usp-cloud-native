// Package analytics contains the application use cases for analytics queries.
// These are read-only use cases — they never write to the analytics store.
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

const tracerName = "github.com/urlshortener/platform/internal/application/analytics"

// QueryRepository is the interface for analytics read operations.
// Implemented by postgres.AnalyticsQueryRepository.
type QueryRepository interface {
	GetSummary(ctx context.Context, shortCode string, start, end time.Time) (*analytics.Summary, error)
	GetTimeSeries(ctx context.Context, shortCode string, start, end time.Time, g analytics.Granularity) (*analytics.TimeSeries, error)
	GetBreakdown(ctx context.Context, shortCode string, start, end time.Time, dim analytics.Dimension) (*analytics.Breakdown, error)
}

// URLReadRepository is used to resolve URL ID → short code.
// Analytics queries use the short code as the key (indexed), not the URL ULID.
type URLReadRepository interface {
	GetByID(ctx context.Context, id, workspaceID string) (*domainurl.URL, error)
}

// SummaryQuery carries inputs for the GetAnalyticsSummary use case.
type SummaryQuery struct {
	// URLID is the ULID of the URL to query.
	URLID       string
	WorkspaceID string
	// Window is a named time window (1h, 24h, 7d, 30d, all).
	Window analytics.Window
}

// SummaryResult is the API-facing summary response.
type SummaryResult struct {
	ShortCode   string
	ShortURL    string
	TotalClicks int64
	UniqueIPs   int64
	BotClicks   int64
	WindowStart time.Time
	WindowEnd   time.Time
	Window      string
}

// SummaryHandler orchestrates the GetAnalyticsSummary use case.
//
// Authorization model:
//
//	We resolve the URL by (URLID, WorkspaceID) — same workspace-scoped
//	fetch used by the URL CRUD handlers. If the URL doesn't belong to
//	the workspace in the JWT claims, we return ErrNotFound (no leakage).
type SummaryHandler struct {
	analyticsRepo QueryRepository
	urlRepo       URLReadRepository
	baseURL       string
}

// NewSummaryHandler creates a SummaryHandler.
func NewSummaryHandler(ar QueryRepository, ur URLReadRepository, baseURL string) *SummaryHandler {
	return &SummaryHandler{analyticsRepo: ar, urlRepo: ur, baseURL: baseURL}
}

// Handle executes the GetAnalyticsSummary use case.
func (h *SummaryHandler) Handle(ctx context.Context, q SummaryQuery) (*SummaryResult, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "GetAnalyticsSummary.Handle",
		attribute.String("url.id", q.URLID),
		attribute.String("analytics.window", string(q.Window)),
	)
	defer span.End()

	if !q.Window.IsValid() {
		return nil, apperrors.NewValidationError(
			"window must be one of: 1h, 24h, 7d, 30d, all", nil)
	}

	// Resolve URL to get short code (and enforce workspace ownership).
	u, err := h.urlRepo.GetByID(ctx, q.URLID, q.WorkspaceID)
	if err != nil {
		if domainurl.IsNotFound(err) {
			return nil, apperrors.ErrNotFound
		}
		return nil, fmt.Errorf("resolving url for analytics: %w", err)
	}

	end := time.Now().UTC()
	var start time.Time
	if q.Window == analytics.WindowAllTime {
		start = u.CreatedAt // start from URL creation
	} else {
		start = end.Add(-q.Window.Duration())
	}

	summary, err := h.analyticsRepo.GetSummary(ctx, u.ShortCode, start, end)
	if err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("fetching analytics summary: %w", err)
	}

	return &SummaryResult{
		ShortCode:   u.ShortCode,
		ShortURL:    h.baseURL + "/" + u.ShortCode,
		TotalClicks: summary.TotalClicks,
		UniqueIPs:   summary.UniqueIPs,
		BotClicks:   summary.BotClicks,
		WindowStart: start,
		WindowEnd:   end,
		Window:      string(q.Window),
	}, nil
}
