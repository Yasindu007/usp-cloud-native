package handler

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	appanalytics "github.com/urlshortener/platform/internal/application/analytics"
	"github.com/urlshortener/platform/internal/application/apperrors"
	"github.com/urlshortener/platform/internal/domain/analytics"
	domainauth "github.com/urlshortener/platform/internal/domain/auth"
	"github.com/urlshortener/platform/internal/interfaces/http/response"
	"github.com/urlshortener/platform/pkg/logger"

	"errors"
)

// ── Use case interfaces ────────────────────────────────────────────────────────

type AnalyticsSummarizer interface {
	Handle(ctx context.Context, q appanalytics.SummaryQuery) (*appanalytics.SummaryResult, error)
}

type AnalyticsTimeSeriesGetter interface {
	Handle(ctx context.Context, q appanalytics.TimeSeriesQuery) (*appanalytics.TimeSeriesResult, error)
}

type AnalyticsBreakdownGetter interface {
	Handle(ctx context.Context, q appanalytics.BreakdownQuery) (*appanalytics.BreakdownResult, error)
}

// ── AnalyticsHandler ──────────────────────────────────────────────────────────

// AnalyticsHandler handles all analytics HTTP endpoints.
type AnalyticsHandler struct {
	summarizer AnalyticsSummarizer
	timeSeries AnalyticsTimeSeriesGetter
	breakdown  AnalyticsBreakdownGetter
	log        *slog.Logger
}

// NewAnalyticsHandler constructs an AnalyticsHandler.
func NewAnalyticsHandler(
	summarizer AnalyticsSummarizer,
	timeSeries AnalyticsTimeSeriesGetter,
	breakdown AnalyticsBreakdownGetter,
	log *slog.Logger,
) *AnalyticsHandler {
	return &AnalyticsHandler{
		summarizer: summarizer,
		timeSeries: timeSeries,
		breakdown:  breakdown,
		log:        log,
	}
}

// ── GET /api/v1/workspaces/{workspaceID}/urls/{urlID}/analytics ───────────────

// GetSummary returns aggregate click counts for a URL.
//
// Query parameters:
//
//	window  — time window: 1h | 24h | 7d | 30d | all (default: 24h)
//
// Example response:
//
//	{
//	  "data": {
//	    "short_code": "abc1234",
//	    "short_url": "https://s.example.com/abc1234",
//	    "total_clicks": 1423,
//	    "unique_ips": 876,
//	    "bot_clicks": 42,
//	    "window": "24h",
//	    "window_start": "2026-01-23T10:00:00Z",
//	    "window_end":   "2026-01-24T10:00:00Z"
//	  }
//	}
func (h *AnalyticsHandler) GetSummary(w http.ResponseWriter, r *http.Request) {
	claims, ok := domainauth.FromContext(r.Context())
	if !ok {
		response.WriteProblem(w, response.Problem{
			Type: response.ProblemTypeUnauthenticated, Title: "Unauthorized",
			Status: http.StatusUnauthorized,
		})
		return
	}

	urlID := chi.URLParam(r, "urlID")
	windowStr := r.URL.Query().Get("window")
	if windowStr == "" {
		windowStr = "24h"
	}

	window, err := analytics.ParseWindow(windowStr)
	if err != nil {
		response.UnprocessableEntity(w, err.Error(), r.URL.Path)
		return
	}

	result, err := h.summarizer.Handle(r.Context(), appanalytics.SummaryQuery{
		URLID:       urlID,
		WorkspaceID: claims.WorkspaceID,
		Window:      window,
	})
	if err != nil {
		h.writeError(w, r, err, logger.FromContext(r.Context()))
		return
	}

	response.JSON(w, http.StatusOK, response.Envelope{
		Data: map[string]any{
			"short_code":   result.ShortCode,
			"short_url":    result.ShortURL,
			"total_clicks": result.TotalClicks,
			"unique_ips":   result.UniqueIPs,
			"bot_clicks":   result.BotClicks,
			"window":       result.Window,
			"window_start": result.WindowStart.Format(time.RFC3339),
			"window_end":   result.WindowEnd.Format(time.RFC3339),
		},
	})
}

// ── GET /api/v1/workspaces/{workspaceID}/urls/{urlID}/analytics/timeseries ────

// GetTimeSeries returns click counts in time buckets.
//
// Query parameters:
//
//	window      — time window (default: 24h)
//	granularity — bucket size: 1m | 1h | 1d (default: 1h)
//	start       — explicit start time (RFC 3339, overrides window)
//	end         — explicit end time   (RFC 3339, overrides window)
func (h *AnalyticsHandler) GetTimeSeries(w http.ResponseWriter, r *http.Request) {
	claims, ok := domainauth.FromContext(r.Context())
	if !ok {
		response.WriteProblem(w, response.Problem{
			Type: response.ProblemTypeUnauthenticated, Title: "Unauthorized",
			Status: http.StatusUnauthorized,
		})
		return
	}

	urlID := chi.URLParam(r, "urlID")

	// Parse granularity
	granStr := r.URL.Query().Get("granularity")
	if granStr == "" {
		granStr = "1h"
	}
	gran, err := analytics.ParseGranularity(granStr)
	if err != nil {
		response.UnprocessableEntity(w, err.Error(), r.URL.Path)
		return
	}

	q := appanalytics.TimeSeriesQuery{
		URLID:       urlID,
		WorkspaceID: claims.WorkspaceID,
		Granularity: gran,
	}

	// Parse optional explicit time bounds (override window)
	if startStr := r.URL.Query().Get("start"); startStr != "" {
		t, parseErr := time.Parse(time.RFC3339, startStr)
		if parseErr != nil {
			response.UnprocessableEntity(w, "start must be RFC 3339 formatted", r.URL.Path)
			return
		}
		q.Start = t
	}
	if endStr := r.URL.Query().Get("end"); endStr != "" {
		t, parseErr := time.Parse(time.RFC3339, endStr)
		if parseErr != nil {
			response.UnprocessableEntity(w, "end must be RFC 3339 formatted", r.URL.Path)
			return
		}
		q.End = t
	}

	// Fall back to named window when explicit bounds not given
	if q.Start.IsZero() {
		windowStr := r.URL.Query().Get("window")
		if windowStr == "" {
			windowStr = "24h"
		}
		window, wErr := analytics.ParseWindow(windowStr)
		if wErr != nil {
			response.UnprocessableEntity(w, wErr.Error(), r.URL.Path)
			return
		}
		q.Window = window
	}

	result, err := h.timeSeries.Handle(r.Context(), q)
	if err != nil {
		h.writeError(w, r, err, logger.FromContext(r.Context()))
		return
	}

	// Serialise points
	points := make([]map[string]any, 0, len(result.Points))
	for _, p := range result.Points {
		points = append(points, map[string]any{
			"bucket_start": p.BucketStart.Format(time.RFC3339),
			"clicks":       p.Clicks,
			"unique_ips":   p.UniqueIPs,
		})
	}

	response.JSON(w, http.StatusOK, response.Envelope{
		Data: map[string]any{
			"short_code":   result.ShortCode,
			"granularity":  result.Granularity,
			"window_start": result.WindowStart.Format(time.RFC3339),
			"window_end":   result.WindowEnd.Format(time.RFC3339),
			"points":       points,
		},
	})
}

// ── GET /api/v1/workspaces/{workspaceID}/urls/{urlID}/analytics/breakdown ──────

// GetBreakdown returns click counts grouped by a dimension.
//
// Query parameters:
//
//	dimension — country | device_type | browser_family | os_family | referrer_domain
//	window    — time window (default: 7d)
func (h *AnalyticsHandler) GetBreakdown(w http.ResponseWriter, r *http.Request) {
	claims, ok := domainauth.FromContext(r.Context())
	if !ok {
		response.WriteProblem(w, response.Problem{
			Type: response.ProblemTypeUnauthenticated, Title: "Unauthorized",
			Status: http.StatusUnauthorized,
		})
		return
	}

	urlID := chi.URLParam(r, "urlID")

	dimStr := r.URL.Query().Get("dimension")
	if dimStr == "" {
		dimStr = "country"
	}
	dim, err := analytics.ParseDimension(dimStr)
	if err != nil {
		response.UnprocessableEntity(w, err.Error(), r.URL.Path)
		return
	}

	windowStr := r.URL.Query().Get("window")
	if windowStr == "" {
		windowStr = "7d"
	}
	window, err := analytics.ParseWindow(windowStr)
	if err != nil {
		response.UnprocessableEntity(w, err.Error(), r.URL.Path)
		return
	}

	result, err := h.breakdown.Handle(r.Context(), appanalytics.BreakdownQuery{
		URLID:       urlID,
		WorkspaceID: claims.WorkspaceID,
		Window:      window,
		Dimension:   dim,
	})
	if err != nil {
		h.writeError(w, r, err, logger.FromContext(r.Context()))
		return
	}

	counts := make([]map[string]any, 0, len(result.Counts))
	for _, c := range result.Counts {
		counts = append(counts, map[string]any{
			"value":      c.Value,
			"clicks":     c.Clicks,
			"percentage": c.Percentage,
		})
	}

	response.JSON(w, http.StatusOK, response.Envelope{
		Data: map[string]any{
			"short_code":   result.ShortCode,
			"dimension":    result.Dimension,
			"total_clicks": result.TotalClicks,
			"window_start": result.WindowStart.Format(time.RFC3339),
			"window_end":   result.WindowEnd.Format(time.RFC3339),
			"counts":       counts,
		},
	})
}

// ── Error mapping ─────────────────────────────────────────────────────────────

func (h *AnalyticsHandler) writeError(
	w http.ResponseWriter, r *http.Request, err error, log *slog.Logger,
) {
	var ve *apperrors.ValidationError
	if errors.As(err, &ve) {
		response.UnprocessableEntity(w, ve.Message, r.URL.Path)
		return
	}
	if errors.Is(err, apperrors.ErrNotFound) {
		response.NotFound(w, r.URL.Path)
		return
	}
	log.Error("unexpected error in analytics handler",
		slog.String("error", err.Error()),
		slog.String("path", r.URL.Path),
	)
	response.InternalError(w, r.URL.Path)
}
