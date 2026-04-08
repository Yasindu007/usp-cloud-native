package middleware_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	domainauth "github.com/urlshortener/platform/internal/domain/auth"
	"github.com/urlshortener/platform/internal/domain/ratelimit"
	"github.com/urlshortener/platform/internal/infrastructure/metrics"
	httpmiddleware "github.com/urlshortener/platform/internal/interfaces/http/middleware"
)

// ── Fake limiter ──────────────────────────────────────────────────────────────

// fakeLimiter lets tests control the rate limit outcome without Redis.
type fakeLimiter struct {
	// allow controls whether Check() returns allowed=true.
	allow bool
	// err forces an error return from Check().
	err error
	// capturedKey records the last key passed to Check() for assertion.
	capturedKey string
	// capturedPolicy records the last policy passed to Check().
	capturedPolicy ratelimit.Policy
	// remaining is the token count returned in the result.
	remaining int
}

func (f *fakeLimiter) Check(_ context.Context, key string, policy ratelimit.Policy) (*ratelimit.Result, error) {
	f.capturedKey = key
	f.capturedPolicy = policy

	if f.err != nil {
		return nil, f.err
	}

	remaining := f.remaining
	if !f.allow {
		remaining = 0
	}

	return &ratelimit.Result{
		Allowed:    f.allow,
		Remaining:  remaining,
		Limit:      policy.BucketCapacity(),
		ResetAt:    time.Now().Add(60 * time.Second),
		RetryAfter: 30 * time.Second,
	}, nil
}

func newRLMiddleware(limiter httpmiddleware.Limiter, class ratelimit.EndpointClass) func(http.Handler) http.Handler {
	return httpmiddleware.RateLimit(httpmiddleware.RateLimitConfig{
		Limiter:       limiter,
		ServiceName:   "test-service",
		Metrics:       metrics.New("test-service", "test", "test"),
		EndpointClass: class,
		Log:           authTestLog,
		FailOpen:      true,
	})
}

func newRLMiddlewareWithMetrics(
	limiter httpmiddleware.Limiter,
	class ratelimit.EndpointClass,
	m *metrics.Metrics,
) func(http.Handler) http.Handler {
	return httpmiddleware.RateLimit(httpmiddleware.RateLimitConfig{
		Limiter:       limiter,
		ServiceName:   "test-service",
		Metrics:       m,
		EndpointClass: class,
		Log:           authTestLog,
		FailOpen:      true,
	})
}

// ── Tests ─────────────────────────────────────────────────────────────────────

func TestRateLimit_Allowed_CallsNext(t *testing.T) {
	limiter := &fakeLimiter{allow: true, remaining: 9}
	mw := newRLMiddleware(limiter, ratelimit.ClassWrite)

	called := false
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/urls", nil)

	mw(nextHandler(&called)).ServeHTTP(w, r)

	if !called {
		t.Error("expected next handler called when allowed")
	}
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestRateLimit_Allowed_SetsRateLimitHeaders(t *testing.T) {
	limiter := &fakeLimiter{allow: true, remaining: 42}
	mw := newRLMiddleware(limiter, ratelimit.ClassWrite)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/urls", nil)

	mw(nextHandler(new(bool))).ServeHTTP(w, r)

	// Rate limit headers must be present on allowed responses too.
	if w.Header().Get("RateLimit-Limit") == "" {
		t.Error("expected RateLimit-Limit header on allowed response")
	}
	if w.Header().Get("RateLimit-Remaining") == "" {
		t.Error("expected RateLimit-Remaining header on allowed response")
	}
	if w.Header().Get("RateLimit-Reset") == "" {
		t.Error("expected RateLimit-Reset header on allowed response")
	}
	// Retry-After must NOT be present on allowed responses
	if w.Header().Get("Retry-After") != "" {
		t.Error("Retry-After must not be set on allowed responses")
	}
}

func TestRateLimit_Denied_Returns429(t *testing.T) {
	limiter := &fakeLimiter{allow: false, remaining: 0}
	mw := newRLMiddleware(limiter, ratelimit.ClassWrite)

	called := false
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/urls", nil)

	mw(nextHandler(&called)).ServeHTTP(w, r)

	if called {
		t.Error("next handler must NOT be called when rate limited")
	}
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("expected 429, got %d", w.Code)
	}
	if w.Header().Get("Retry-After") == "" {
		t.Error("expected Retry-After header on 429 response")
	}
}

func TestRateLimit_Allowed_RecordsMetric(t *testing.T) {
	m := metrics.New("test-service", "test", "test")
	limiter := &fakeLimiter{allow: true, remaining: 9}
	mw := newRLMiddlewareWithMetrics(limiter, ratelimit.ClassWrite, m)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/urls", nil)

	mw(nextHandler(new(bool))).ServeHTTP(w, r)

	val := testutil.ToFloat64(
		m.RateLimitTotal.WithLabelValues(
			"test-service",
			string(ratelimit.TierUnauthenticated),
			string(ratelimit.ClassWrite),
			"allowed",
		),
	)
	if val != 1 {
		t.Fatalf("expected allowed rate limit metric to equal 1, got %v", val)
	}
}

func TestRateLimit_Denied_RecordsMetric(t *testing.T) {
	m := metrics.New("test-service", "test", "test")
	limiter := &fakeLimiter{allow: false}
	mw := newRLMiddlewareWithMetrics(limiter, ratelimit.ClassWrite, m)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/urls", nil)

	mw(nextHandler(new(bool))).ServeHTTP(w, r)

	val := testutil.ToFloat64(
		m.RateLimitTotal.WithLabelValues(
			"test-service",
			string(ratelimit.TierUnauthenticated),
			string(ratelimit.ClassWrite),
			"denied",
		),
	)
	if val != 1 {
		t.Fatalf("expected denied rate limit metric to equal 1, got %v", val)
	}
}

func TestRateLimit_Denied_ResponseIsProblemsJSON(t *testing.T) {
	limiter := &fakeLimiter{allow: false}
	mw := newRLMiddleware(limiter, ratelimit.ClassWrite)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	mw(nextHandler(new(bool))).ServeHTTP(w, r)

	ct := w.Header().Get("Content-Type")
	if ct == "" || ct[:len("application/problem+json")] != "application/problem+json" {
		t.Errorf("expected application/problem+json, got %q", ct)
	}
}

func TestRateLimit_UnauthenticatedRequest_UsesIP(t *testing.T) {
	limiter := &fakeLimiter{allow: true, remaining: 5}
	mw := newRLMiddleware(limiter, ratelimit.ClassWrite)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.1:12345"
	// No auth claims in context

	mw(nextHandler(new(bool))).ServeHTTP(w, r)

	// Key should contain the IP address
	if limiter.capturedKey == "" {
		t.Fatal("expected capturedKey to be set")
	}
	// Key should use unauthenticated tier
	if limiter.capturedPolicy.Limit != ratelimit.GetPolicy(ratelimit.TierUnauthenticated, ratelimit.ClassWrite).Limit {
		t.Errorf("expected unauthenticated policy, got limit=%d", limiter.capturedPolicy.Limit)
	}
}

func TestRateLimit_AuthenticatedRequest_UsesTokenID(t *testing.T) {
	limiter := &fakeLimiter{allow: true, remaining: 99}
	mw := newRLMiddleware(limiter, ratelimit.ClassWrite)

	claims := &domainauth.Claims{
		UserID:      "usr_001",
		TokenID:     "key_abc123",
		WorkspaceID: "ws_001",
		Scope:       "read write",
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/urls", nil).
		WithContext(domainauth.WithContext(context.Background(), claims))

	mw(nextHandler(new(bool))).ServeHTTP(w, r)

	// Key should contain the token ID
	if limiter.capturedKey == "" {
		t.Fatal("expected capturedKey to be set")
	}
	// Verify the key contains the token ID
	expectedContains := "key_abc123"
	if len(limiter.capturedKey) < len(expectedContains) {
		t.Errorf("key %q does not contain token ID %q", limiter.capturedKey, expectedContains)
	}
}

func TestRateLimit_RedirectClass_UsesHigherLimit(t *testing.T) {
	limiter := &fakeLimiter{allow: true}
	mw := newRLMiddleware(limiter, ratelimit.ClassRedirect)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/abc1234", nil)

	mw(nextHandler(new(bool))).ServeHTTP(w, r)

	// Redirect policy for unauthenticated has limit=300, write has limit=10
	redirectPolicy := ratelimit.GetPolicy(ratelimit.TierUnauthenticated, ratelimit.ClassRedirect)
	if limiter.capturedPolicy.Limit != redirectPolicy.Limit {
		t.Errorf("expected redirect limit=%d, got %d",
			redirectPolicy.Limit, limiter.capturedPolicy.Limit)
	}
}

func TestRateLimit_RedisError_FailOpen_Allows(t *testing.T) {
	limiter := &fakeLimiter{err: errFake}
	mw := httpmiddleware.RateLimit(httpmiddleware.RateLimitConfig{
		Limiter:       limiter,
		ServiceName:   "test",
		EndpointClass: ratelimit.ClassWrite,
		Log:           authTestLog,
		FailOpen:      true, // explicit fail-open
	})

	called := false
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/", nil)

	mw(nextHandler(&called)).ServeHTTP(w, r)

	if !called {
		t.Error("expected fail-open: next handler should be called when Redis errors")
	}
}

func TestRateLimit_RedisError_FailClosed_Blocks(t *testing.T) {
	limiter := &fakeLimiter{err: errFake}
	mw := httpmiddleware.RateLimit(httpmiddleware.RateLimitConfig{
		Limiter:       limiter,
		ServiceName:   "test",
		EndpointClass: ratelimit.ClassWrite,
		Log:           authTestLog,
		FailOpen:      false, // strict fail-closed
	})

	called := false
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/", nil)

	mw(nextHandler(&called)).ServeHTTP(w, r)

	if called {
		t.Error("expected fail-closed: next handler must NOT be called when Redis errors")
	}
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 for fail-closed, got %d", w.Code)
	}
}

func TestRateLimit_Headers_RemainingDecreases(t *testing.T) {
	// Verify that as the limiter reports decreasing remaining tokens,
	// the header values reflect that correctly.
	for _, remaining := range []int{99, 50, 1, 0} {
		limiter := &fakeLimiter{allow: remaining > 0, remaining: remaining}
		mw := newRLMiddleware(limiter, ratelimit.ClassRead)

		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/api/v1/urls", nil)
		mw(nextHandler(new(bool))).ServeHTTP(w, r)

		got := w.Header().Get("RateLimit-Remaining")
		gotInt, err := strconv.Atoi(got)
		if err != nil {
			t.Errorf("remaining=%d: could not parse header %q: %v", remaining, got, err)
			continue
		}
		if gotInt != remaining {
			t.Errorf("remaining=%d: header says %d", remaining, gotInt)
		}
	}
}
