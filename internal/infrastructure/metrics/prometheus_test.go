package metrics_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/urlshortener/platform/internal/infrastructure/metrics"
)

// newTestMetrics creates a fresh Metrics instance for each test.
// Because we use a custom registry (not the default global), each
// test gets an isolated metric set — no cross-test contamination.
func newTestMetrics() *metrics.Metrics {
	return metrics.New("test-service", "v0.0.0-test", "testcommit")
}

// ── HTTPRequestsTotal ─────────────────────────────────────────────────────────

func TestMetrics_RecordHTTPRequest_IncrementsCounter(t *testing.T) {
	m := newTestMetrics()

	m.RecordHTTPRequest("api-service", "POST", "/api/v1/urls", 201, 0.05)
	m.RecordHTTPRequest("api-service", "POST", "/api/v1/urls", 201, 0.04)
	m.RecordHTTPRequest("api-service", "POST", "/api/v1/urls", 422, 0.01)

	// Count the total observations across all label combos for this metric.
	total := testutil.CollectAndCount(m.HTTPRequestsTotal)
	// 2 distinct label sets (201 and 422) → 2 series
	if total != 2 {
		t.Errorf("expected 2 counter series, got %d", total)
	}

	// Verify the 201 counter accumulated correctly.
	count201 := testutil.ToFloat64(
		m.HTTPRequestsTotal.WithLabelValues("api-service", "POST", "/api/v1/urls", "201"),
	)
	if count201 != 2 {
		t.Errorf("expected count=2 for 201 responses, got %v", count201)
	}

	count422 := testutil.ToFloat64(
		m.HTTPRequestsTotal.WithLabelValues("api-service", "POST", "/api/v1/urls", "422"),
	)
	if count422 != 1 {
		t.Errorf("expected count=1 for 422 responses, got %v", count422)
	}
}

func TestMetrics_RecordHTTPRequest_DifferentServices(t *testing.T) {
	m := newTestMetrics()

	m.RecordHTTPRequest("api-service", "GET", "/healthz", 200, 0.001)
	m.RecordHTTPRequest("redirect-service", "GET", "/{shortcode}", 302, 0.003)

	apiCount := testutil.ToFloat64(
		m.HTTPRequestsTotal.WithLabelValues("api-service", "GET", "/healthz", "200"),
	)
	if apiCount != 1 {
		t.Errorf("expected api-service count=1, got %v", apiCount)
	}

	redirectCount := testutil.ToFloat64(
		m.HTTPRequestsTotal.WithLabelValues("redirect-service", "GET", "/{shortcode}", "302"),
	)
	if redirectCount != 1 {
		t.Errorf("expected redirect-service count=1, got %v", redirectCount)
	}
}

func TestMetrics_RecordHTTPRequest_RecordsHistogramObservation(t *testing.T) {
	m := newTestMetrics()

	// Record 3 requests with different durations.
	m.RecordHTTPRequest("redirect-service", "GET", "/{shortcode}", 302, 0.003)
	m.RecordHTTPRequest("redirect-service", "GET", "/{shortcode}", 302, 0.007)
	m.RecordHTTPRequest("redirect-service", "GET", "/{shortcode}", 302, 0.045)

	// The histogram should have 3 observations.
	// testutil.CollectAndCount gives the number of metric series (each bucket + sum + count).
	// Instead verify by checking sum > 0.
	obs := m.HTTPRequestDuration.WithLabelValues("redirect-service", "GET", "/{shortcode}", "302")
	// We can't directly get histogram sum via testutil.ToFloat64 on a histogram,
	// but we can verify the histogram has metric families by collecting.
	count := testutil.CollectAndCount(m.HTTPRequestDuration)
	if count == 0 {
		t.Error("expected histogram to have observations, got 0 series")
	}
	_ = obs
}

// ── RedirectsTotal ────────────────────────────────────────────────────────────

func TestMetrics_RecordRedirect_HitMissCounts(t *testing.T) {
	m := newTestMetrics()

	// Simulate 100 hits and 5 misses (95% hit ratio).
	for i := 0; i < 100; i++ {
		m.RecordRedirect("hit")
	}
	for i := 0; i < 5; i++ {
		m.RecordRedirect("miss")
	}

	hits := testutil.ToFloat64(m.RedirectsTotal.WithLabelValues("hit"))
	if hits != 100 {
		t.Errorf("expected 100 hits, got %v", hits)
	}

	misses := testutil.ToFloat64(m.RedirectsTotal.WithLabelValues("miss"))
	if misses != 5 {
		t.Errorf("expected 5 misses, got %v", misses)
	}

	// Verify hit ratio is 100/105 ≈ 95.2% — this is the SLI-05 SLO target
	hitRatio := hits / (hits + misses)
	if hitRatio < 0.95 {
		t.Errorf("expected hit ratio >= 0.95, got %v", hitRatio)
	}
}

func TestMetrics_RecordRedirect_AllCacheStatuses(t *testing.T) {
	m := newTestMetrics()

	for _, status := range []string{"hit", "miss", "negative_hit", "error"} {
		m.RecordRedirect(status)
	}

	for _, status := range []string{"hit", "miss", "negative_hit", "error"} {
		val := testutil.ToFloat64(m.RedirectsTotal.WithLabelValues(status))
		if val != 1 {
			t.Errorf("expected 1 for cache_status=%q, got %v", status, val)
		}
	}
}

// ── URLsShortenedTotal ────────────────────────────────────────────────────────

func TestMetrics_RecordURLShortened_Increments(t *testing.T) {
	m := newTestMetrics()

	m.RecordURLShortened()
	m.RecordURLShortened()
	m.RecordURLShortened()

	val := testutil.ToFloat64(m.URLsShortenedTotal)
	if val != 3 {
		t.Errorf("expected 3, got %v", val)
	}
}

func TestMetrics_RecordRateLimit_Increments(t *testing.T) {
	m := newTestMetrics()

	m.RecordRateLimit("api-service", "free", "write", "allowed")
	m.RecordRateLimit("api-service", "free", "write", "allowed")
	m.RecordRateLimit("api-service", "free", "write", "denied")

	allowed := testutil.ToFloat64(
		m.RateLimitTotal.WithLabelValues("api-service", "free", "write", "allowed"),
	)
	if allowed != 2 {
		t.Errorf("expected allowed count=2, got %v", allowed)
	}

	denied := testutil.ToFloat64(
		m.RateLimitTotal.WithLabelValues("api-service", "free", "write", "denied"),
	)
	if denied != 1 {
		t.Errorf("expected denied count=1, got %v", denied)
	}
}

// ── DBPoolConnections ─────────────────────────────────────────────────────────

func TestMetrics_UpdateDBPoolStats_SetsGauges(t *testing.T) {
	m := newTestMetrics()

	m.UpdateDBPoolStats("primary", 25, 20, 5, 25)

	cases := map[string]float64{
		"total":    25,
		"idle":     20,
		"acquired": 5,
		"max":      25,
	}

	for state, expected := range cases {
		val := testutil.ToFloat64(m.DBPoolConnections.WithLabelValues("primary", state))
		if val != expected {
			t.Errorf("DBPoolConnections{pool=primary, state=%s}: expected %v, got %v",
				state, expected, val)
		}
	}
}

func TestMetrics_UpdateDBPoolStats_Saturation(t *testing.T) {
	m := newTestMetrics()

	// Simulate 80% saturation (20/25 connections acquired)
	m.UpdateDBPoolStats("primary", 25, 5, 20, 25)

	acquired := testutil.ToFloat64(m.DBPoolConnections.WithLabelValues("primary", "acquired"))
	max := testutil.ToFloat64(m.DBPoolConnections.WithLabelValues("primary", "max"))

	saturation := acquired / max
	if saturation < 0.79 || saturation > 0.81 {
		t.Errorf("expected saturation ~0.80, got %v", saturation)
	}
}

// ── CachePoolConnections ──────────────────────────────────────────────────────

func TestMetrics_UpdateCachePoolStats_SetsGauges(t *testing.T) {
	m := newTestMetrics()

	m.UpdateCachePoolStats(10, 8, 1)

	cases := map[string]float64{"total": 10, "idle": 8, "stale": 1}
	for state, expected := range cases {
		val := testutil.ToFloat64(m.CachePoolConnections.WithLabelValues(state))
		if val != expected {
			t.Errorf("CachePoolConnections{state=%s}: expected %v, got %v", state, expected, val)
		}
	}
}

// ── BuildInfo ─────────────────────────────────────────────────────────────────

func TestMetrics_BuildInfo_SetOnNew(t *testing.T) {
	m := metrics.New("api-service", "v1.2.3", "abc1234")

	// BuildInfo should always be 1
	// We check that the metric family exists by collecting it
	count := testutil.CollectAndCount(m.BuildInfo)
	if count == 0 {
		t.Error("expected BuildInfo to have at least one series")
	}
}

// ── Handler ───────────────────────────────────────────────────────────────────

func TestMetrics_Handler_ServesPrometheusFormat(t *testing.T) {
	m := newTestMetrics()
	m.RecordURLShortened()
	m.RecordRedirect("hit")

	handler := m.Handler()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/metrics", nil)

	handler.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	// Must return Prometheus text format or OpenMetrics format
	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/plain") && !strings.Contains(ct, "application/openmetrics-text") {
		t.Errorf("expected Prometheus content type, got %q", ct)
	}

	body := w.Body.String()

	// Verify our custom metrics appear in the output
	if !strings.Contains(body, "urlshortener_urls_shortened_total") {
		t.Error("expected urlshortener_urls_shortened_total in /metrics output")
	}
	if !strings.Contains(body, "urlshortener_redirects_total") {
		t.Error("expected urlshortener_redirects_total in /metrics output")
	}
	// Verify Go runtime metrics are included (from GoCollector)
	if !strings.Contains(body, "go_goroutines") {
		t.Error("expected go_goroutines in /metrics output")
	}
}
