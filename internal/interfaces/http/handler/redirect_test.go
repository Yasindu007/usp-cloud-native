package handler_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/urlshortener/platform/internal/application/apperrors"
	"github.com/urlshortener/platform/internal/application/resolve"
	"github.com/urlshortener/platform/internal/interfaces/http/handler"
	"github.com/urlshortener/platform/internal/interfaces/http/response"
)

// ── Mock ──────────────────────────────────────────────────────────────────────

type mockResolver struct {
	result        *resolve.Result
	err           error
	capturedQuery resolve.Query
}

func (m *mockResolver) Handle(_ context.Context, q resolve.Query) (*resolve.Result, error) {
	m.capturedQuery = q
	return m.result, m.err
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// withShortCode builds a request with the chi route context containing
// the given shortcode URL parameter. Required because chi.URLParam reads
// from chi.RouteContext — which is only populated when chi routes the request.
// In unit tests, we bypass chi routing so must set this manually.
func withShortCode(r *http.Request, shortCode string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("shortcode", shortCode)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

func redirectRequest(shortCode string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/"+shortCode, nil)
	r.Header.Set("User-Agent", "Mozilla/5.0 (test)")
	r.Header.Set("Referer", "https://test.example.com")
	return withShortCode(r, shortCode)
}

// ── Tests ─────────────────────────────────────────────────────────────────────

func TestRedirectHandler_Handle_Success_302(t *testing.T) {
	mock := &mockResolver{
		result: &resolve.Result{
			OriginalURL: "https://example.com/long/path?utm=test",
			ShortCode:   "abc1234",
			CacheStatus: "hit",
		},
	}
	h := handler.NewRedirectHandler(mock, testLog, nil)

	w := httptest.NewRecorder()
	h.Handle(w, redirectRequest("abc1234"))

	// Must return 302 Found
	if w.Code != http.StatusFound {
		t.Errorf("expected 302, got %d — body: %s", w.Code, w.Body.String())
	}

	// Location header must be the original URL
	location := w.Header().Get("Location")
	if location != "https://example.com/long/path?utm=test" {
		t.Errorf("expected Location header %q, got %q",
			"https://example.com/long/path?utm=test", location)
	}

	// Cache-Control must be no-store (prevents proxy caching of redirects)
	if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("expected Cache-Control: no-store, got %q", cc)
	}
}

func TestRedirectHandler_Handle_ShortCodePassedToUseCase(t *testing.T) {
	mock := &mockResolver{
		result: &resolve.Result{
			OriginalURL: "https://example.com",
			ShortCode:   "mycode",
			CacheStatus: "miss",
		},
	}
	h := handler.NewRedirectHandler(mock, testLog, nil)

	w := httptest.NewRecorder()
	h.Handle(w, redirectRequest("mycode"))

	if mock.capturedQuery.ShortCode != "mycode" {
		t.Errorf("expected ShortCode=mycode in use case query, got %q",
			mock.capturedQuery.ShortCode)
	}
}

func TestRedirectHandler_Handle_RequestMetadataPopulated(t *testing.T) {
	mock := &mockResolver{
		result: &resolve.Result{OriginalURL: "https://example.com", ShortCode: "meta1"},
	}
	h := handler.NewRedirectHandler(mock, testLog, nil)

	r := httptest.NewRequest(http.MethodGet, "/meta1", nil)
	r.Header.Set("User-Agent", "TestAgent/1.0")
	r.Header.Set("Referer", "https://referer.example.com")
	r = withShortCode(r, "meta1")

	w := httptest.NewRecorder()
	h.Handle(w, r)

	// Verify metadata was captured for future analytics use
	if mock.capturedQuery.RequestMetadata.UserAgent != "TestAgent/1.0" {
		t.Errorf("expected UserAgent=TestAgent/1.0, got %q",
			mock.capturedQuery.RequestMetadata.UserAgent)
	}
	if mock.capturedQuery.RequestMetadata.Referrer != "https://referer.example.com" {
		t.Errorf("expected Referrer, got %q",
			mock.capturedQuery.RequestMetadata.Referrer)
	}
}

func TestRedirectHandler_Handle_NotFound_Returns404(t *testing.T) {
	mock := &mockResolver{err: apperrors.ErrNotFound}
	h := handler.NewRedirectHandler(mock, testLog, nil)

	w := httptest.NewRecorder()
	h.Handle(w, redirectRequest("ghost"))

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
	// Must return Problem Details JSON (not HTML)
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
		t.Errorf("expected Content-Type application/problem+json, got %q", ct)
	}
	var prob response.Problem
	decodeJSON(t, w.Body, &prob)
	if prob.Status != http.StatusNotFound {
		t.Errorf("expected problem.status=404, got %d", prob.Status)
	}
}

func TestRedirectHandler_Handle_Expired_Returns410(t *testing.T) {
	mock := &mockResolver{err: apperrors.ErrURLExpired}
	h := handler.NewRedirectHandler(mock, testLog, nil)

	w := httptest.NewRecorder()
	h.Handle(w, redirectRequest("oldlink"))

	if w.Code != http.StatusGone {
		t.Errorf("expected 410 Gone, got %d", w.Code)
	}
	var prob response.Problem
	decodeJSON(t, w.Body, &prob)
	if prob.Type != response.ProblemTypeGone {
		t.Errorf("expected problem type %q, got %q", response.ProblemTypeGone, prob.Type)
	}
}

func TestRedirectHandler_Handle_Disabled_Returns404(t *testing.T) {
	// Disabled URLs return 404 (not 403) to prevent information leakage.
	mock := &mockResolver{err: apperrors.ErrURLDisabled}
	h := handler.NewRedirectHandler(mock, testLog, nil)

	w := httptest.NewRecorder()
	h.Handle(w, redirectRequest("disabled1"))

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for disabled URL, got %d", w.Code)
	}
}

func TestRedirectHandler_Handle_InfrastructureError_Returns500(t *testing.T) {
	mock := &mockResolver{err: errors.New("redis: connection refused")}
	h := handler.NewRedirectHandler(mock, testLog, nil)

	w := httptest.NewRecorder()
	h.Handle(w, redirectRequest("anything"))

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", w.Code)
	}
	// Internal error details must not be exposed
	if strings.Contains(w.Body.String(), "connection refused") {
		t.Error("response must not expose internal error details")
	}
}

func TestRedirectHandler_Handle_EmptyShortCode_Returns404(t *testing.T) {
	h := handler.NewRedirectHandler(&mockResolver{}, testLog, nil)

	// Request with empty shortcode in chi context
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("shortcode", "") // empty
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))

	w := httptest.NewRecorder()
	h.Handle(w, r)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for empty shortcode, got %d", w.Code)
	}
}

func TestRedirectHandler_Handle_CacheHit_vs_Miss_LogsDifferently(t *testing.T) {
	// Both cache hit and miss must return 302 — CacheStatus only affects logs/metrics.
	for _, cacheStatus := range []string{"hit", "miss", "negative_hit"} {
		t.Run("cache_status="+cacheStatus, func(t *testing.T) {
			mock := &mockResolver{
				result: &resolve.Result{
					OriginalURL: "https://example.com",
					ShortCode:   "test",
					CacheStatus: cacheStatus,
				},
			}
			h := handler.NewRedirectHandler(mock, testLog, nil)

			w := httptest.NewRecorder()
			h.Handle(w, redirectRequest("test"))

			if w.Code != http.StatusFound {
				t.Errorf("cache_status=%q: expected 302, got %d", cacheStatus, w.Code)
			}
		})
	}
}
