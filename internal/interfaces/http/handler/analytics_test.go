package handler_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	appanalytics "github.com/urlshortener/platform/internal/application/analytics"
	"github.com/urlshortener/platform/internal/application/apperrors"
	domainauth "github.com/urlshortener/platform/internal/domain/auth"
	"github.com/urlshortener/platform/internal/interfaces/http/handler"
)

// ── Mocks ─────────────────────────────────────────────────────────────────────

type mockSummarizer struct {
	result *appanalytics.SummaryResult
	err    error
}

func (m *mockSummarizer) Handle(_ context.Context, _ appanalytics.SummaryQuery) (*appanalytics.SummaryResult, error) {
	return m.result, m.err
}

type mockTimeSeriesGetter struct {
	result *appanalytics.TimeSeriesResult
	err    error
}

func (m *mockTimeSeriesGetter) Handle(_ context.Context, _ appanalytics.TimeSeriesQuery) (*appanalytics.TimeSeriesResult, error) {
	return m.result, m.err
}

type mockBreakdownGetter struct {
	result *appanalytics.BreakdownResult
	err    error
}

func (m *mockBreakdownGetter) Handle(_ context.Context, _ appanalytics.BreakdownQuery) (*appanalytics.BreakdownResult, error) {
	return m.result, m.err
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func newAnalyticsHandler(
	s handler.AnalyticsSummarizer,
	ts handler.AnalyticsTimeSeriesGetter,
	b handler.AnalyticsBreakdownGetter,
) *handler.AnalyticsHandler {
	return handler.NewAnalyticsHandler(s, ts, b, testLog)
}

func withAnalyticsClaims(r *http.Request) *http.Request {
	claims := &domainauth.Claims{
		UserID:      "usr_001",
		WorkspaceID: "ws_001",
		Scope:       "read write",
	}
	return r.WithContext(domainauth.WithContext(r.Context(), claims))
}

func withAnalyticsURLID(r *http.Request, urlID string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("workspaceID", "ws_001")
	rctx.URLParams.Add("urlID", urlID)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

// ── Summary tests ─────────────────────────────────────────────────────────────

func TestAnalyticsHandler_GetSummary_Success(t *testing.T) {
	mock := &mockSummarizer{
		result: &appanalytics.SummaryResult{
			ShortCode:   "abc1234",
			ShortURL:    "https://s.example.com/abc1234",
			TotalClicks: 1423,
			UniqueIPs:   876,
			BotClicks:   42,
			Window:      "24h",
			WindowStart: time.Now().Add(-24 * time.Hour),
			WindowEnd:   time.Now(),
		},
	}
	h := newAnalyticsHandler(mock, nil, nil)

	w := httptest.NewRecorder()
	r := withAnalyticsClaims(withAnalyticsURLID(
		httptest.NewRequest(http.MethodGet, "/analytics?window=24h", nil),
		"url_001",
	))

	h.GetSummary(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d — body: %s", w.Code, w.Body.String())
	}

	var env struct {
		Data map[string]any `json:"data"`
	}
	json.NewDecoder(w.Body).Decode(&env)
	if env.Data["total_clicks"].(float64) != 1423 {
		t.Errorf("expected total_clicks=1423, got %v", env.Data["total_clicks"])
	}
	if env.Data["short_code"] != "abc1234" {
		t.Errorf("expected short_code=abc1234, got %v", env.Data["short_code"])
	}
}

func TestAnalyticsHandler_GetSummary_DefaultWindow(t *testing.T) {
	mock := &mockSummarizer{result: &appanalytics.SummaryResult{Window: "24h"}}
	h := newAnalyticsHandler(mock, nil, nil)

	w := httptest.NewRecorder()
	r := withAnalyticsClaims(withAnalyticsURLID(
		httptest.NewRequest(http.MethodGet, "/analytics", nil), // no window param
		"url_001",
	))

	h.GetSummary(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 with default window, got %d", w.Code)
	}
}

func TestAnalyticsHandler_GetSummary_InvalidWindow_Returns422(t *testing.T) {
	h := newAnalyticsHandler(&mockSummarizer{}, nil, nil)

	w := httptest.NewRecorder()
	r := withAnalyticsClaims(withAnalyticsURLID(
		httptest.NewRequest(http.MethodGet, "/analytics?window=2weeks", nil),
		"url_001",
	))

	h.GetSummary(w, r)

	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("expected 422 for invalid window, got %d", w.Code)
	}
}

func TestAnalyticsHandler_GetSummary_NotFound_Returns404(t *testing.T) {
	mock := &mockSummarizer{err: apperrors.ErrNotFound}
	h := newAnalyticsHandler(mock, nil, nil)

	w := httptest.NewRecorder()
	r := withAnalyticsClaims(withAnalyticsURLID(
		httptest.NewRequest(http.MethodGet, "/analytics?window=24h", nil),
		"ghost",
	))

	h.GetSummary(w, r)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestAnalyticsHandler_GetSummary_NoClaims_Returns401(t *testing.T) {
	h := newAnalyticsHandler(&mockSummarizer{}, nil, nil)

	w := httptest.NewRecorder()
	r := withAnalyticsURLID(
		httptest.NewRequest(http.MethodGet, "/analytics", nil),
		"url_001",
	)
	// No claims

	h.GetSummary(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

// ── TimeSeries tests ──────────────────────────────────────────────────────────

func TestAnalyticsHandler_GetTimeSeries_Success(t *testing.T) {
	mock := &mockTimeSeriesGetter{
		result: &appanalytics.TimeSeriesResult{
			ShortCode:   "abc1234",
			Granularity: "1h",
			WindowStart: time.Now().Add(-24 * time.Hour),
			WindowEnd:   time.Now(),
			Points: []appanalytics.TimeSeriesPointResult{
				{BucketStart: time.Now().Add(-2 * time.Hour), Clicks: 10, UniqueIPs: 8},
				{BucketStart: time.Now().Add(-1 * time.Hour), Clicks: 25, UniqueIPs: 20},
			},
		},
	}
	h := newAnalyticsHandler(nil, mock, nil)

	w := httptest.NewRecorder()
	r := withAnalyticsClaims(withAnalyticsURLID(
		httptest.NewRequest(http.MethodGet, "/analytics/timeseries?window=24h&granularity=1h", nil),
		"url_001",
	))

	h.GetTimeSeries(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d — body: %s", w.Code, w.Body.String())
	}

	var env struct {
		Data map[string]any `json:"data"`
	}
	json.NewDecoder(w.Body).Decode(&env)
	points := env.Data["points"].([]any)
	if len(points) != 2 {
		t.Errorf("expected 2 points, got %d", len(points))
	}
}

func TestAnalyticsHandler_GetTimeSeries_InvalidGranularity_Returns422(t *testing.T) {
	h := newAnalyticsHandler(nil, &mockTimeSeriesGetter{}, nil)

	w := httptest.NewRecorder()
	r := withAnalyticsClaims(withAnalyticsURLID(
		httptest.NewRequest(http.MethodGet, "/analytics/timeseries?granularity=5m", nil),
		"url_001",
	))

	h.GetTimeSeries(w, r)

	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("expected 422 for invalid granularity, got %d", w.Code)
	}
}

// ── Breakdown tests ───────────────────────────────────────────────────────────

func TestAnalyticsHandler_GetBreakdown_Success(t *testing.T) {
	mock := &mockBreakdownGetter{
		result: &appanalytics.BreakdownResult{
			ShortCode:   "abc1234",
			Dimension:   "country",
			TotalClicks: 100,
			WindowStart: time.Now().Add(-7 * 24 * time.Hour),
			WindowEnd:   time.Now(),
			Counts: []appanalytics.DimensionCountResult{
				{Value: "US", Clicks: 60, Percentage: 60.0},
				{Value: "GB", Clicks: 40, Percentage: 40.0},
			},
		},
	}
	h := newAnalyticsHandler(nil, nil, mock)

	w := httptest.NewRecorder()
	r := withAnalyticsClaims(withAnalyticsURLID(
		httptest.NewRequest(http.MethodGet, "/analytics/breakdown?dimension=country&window=7d", nil),
		"url_001",
	))

	h.GetBreakdown(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d — body: %s", w.Code, w.Body.String())
	}

	var env struct {
		Data map[string]any `json:"data"`
	}
	json.NewDecoder(w.Body).Decode(&env)
	if env.Data["dimension"] != "country" {
		t.Errorf("expected dimension=country, got %v", env.Data["dimension"])
	}
	if env.Data["total_clicks"].(float64) != 100 {
		t.Errorf("expected total_clicks=100, got %v", env.Data["total_clicks"])
	}
}

func TestAnalyticsHandler_GetBreakdown_InvalidDimension_Returns422(t *testing.T) {
	h := newAnalyticsHandler(nil, nil, &mockBreakdownGetter{})

	w := httptest.NewRecorder()
	r := withAnalyticsClaims(withAnalyticsURLID(
		httptest.NewRequest(http.MethodGet, "/analytics/breakdown?dimension=badfield", nil),
		"url_001",
	))

	h.GetBreakdown(w, r)

	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("expected 422 for invalid dimension, got %d", w.Code)
	}
}

func TestAnalyticsHandler_GetBreakdown_DBError_Returns500(t *testing.T) {
	mock := &mockBreakdownGetter{err: errors.New("db: query timeout")}
	h := newAnalyticsHandler(nil, nil, mock)

	w := httptest.NewRecorder()
	r := withAnalyticsClaims(withAnalyticsURLID(
		httptest.NewRequest(http.MethodGet, "/analytics/breakdown?dimension=country", nil),
		"url_001",
	))

	h.GetBreakdown(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", w.Code)
	}
}
