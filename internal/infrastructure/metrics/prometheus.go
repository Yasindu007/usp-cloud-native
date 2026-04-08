// Package metrics defines and manages all Prometheus metrics for the
// URL Shortener Platform.
//
// Design decisions:
//
//  1. Custom registry (not default global):
//     Using prometheus.NewRegistry() instead of prometheus.DefaultRegisterer
//     prevents metric name collisions between tests and avoids the global
//     state problem. Each service instance gets its own isolated registry.
//     The registry is passed to promhttp.HandlerFor() for the /metrics endpoint.
//
//  2. Metric naming convention (Prometheus best practices):
//     namespace_subsystem_name_unit
//     - namespace:  "urlshortener"
//     - subsystem:  omitted at top level; "http", "db", "cache" for subsystems
//     - unit:       "_total" for counters, "_seconds" for durations, "_ratio" for ratios
//
//  3. Cardinality control:
//     High-cardinality labels are intentionally excluded:
//     - workspace_id on shortened URLs would create O(workspaces) label combos
//     - raw URL paths would create O(unique URLs) label combos
//     - shortcode is NEVER a label — that would be millions of combinations
//     Route patterns (/api/v1/urls, /{shortcode}) are safe because they have
//     bounded cardinality regardless of traffic volume.
//
//  4. Histogram bucket selection:
//     Buckets are chosen to give resolution around our SLO boundaries:
//     - 50ms  = redirect P99 SLO boundary
//     - 200ms = API write P99 SLO boundary
//     Buckets outside these ranges catch outliers for alerting.
package metrics

import (
	"net/http"
	"runtime"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// SLO-aligned histogram buckets (in seconds).
// Provides resolution at both the 50ms redirect SLO and 200ms API write SLO.
var httpDurationBuckets = []float64{
	0.001, 0.005, 0.010, 0.025,
	0.050, // ← redirect P99 SLO boundary
	0.100, 0.150,
	0.200, // ← API write P99 SLO boundary
	0.500, 1.000, 2.500, 5.000,
}

// Metrics holds all registered Prometheus metrics for the platform.
// Instantiate once per service using New() and pass the pointer to
// all components that need to record metrics.
type Metrics struct {
	registry *prometheus.Registry

	// ── HTTP layer ───────────────────────────────────────────────────────────
	// HTTPRequestsTotal counts all HTTP requests, partitioned by service,
	// HTTP method, matched route pattern, and response status code.
	// This is the primary metric for SLI-01 (availability) and SLI-02 (API availability).
	//
	// Labels:
	//   service:     "api-service" | "redirect-service"
	//   method:      "GET" | "POST" | "PATCH" | "DELETE"
	//   path:        chi route pattern — e.g. "/api/v1/urls", "/{shortcode}"
	//   status_code: "200" | "201" | "302" | "404" | "422" | "500" etc.
	HTTPRequestsTotal *prometheus.CounterVec

	// HTTPRequestDuration is the histogram of HTTP request durations.
	// Used for SLI-03 (redirect latency P99) and SLI-04 (API write latency P99).
	// The histogram_quantile() function in PromQL derives P50/P95/P99 from this.
	HTTPRequestDuration *prometheus.HistogramVec

	// ── Redirect layer ───────────────────────────────────────────────────────
	// RedirectsTotal counts redirect resolutions by cache outcome.
	// Used for SLI-05 (cache hit ratio).
	//
	// Labels:
	//   cache_status: "hit" | "miss" | "negative_hit" | "error"
	//
	// Cache hit ratio query:
	//   sum(rate(urlshortener_redirects_total{cache_status="hit"}[5m]))
	//   / sum(rate(urlshortener_redirects_total[5m]))
	RedirectsTotal *prometheus.CounterVec

	// ── URL operations ───────────────────────────────────────────────────────
	// URLsShortenedTotal counts all successful URL shortening operations.
	// Intentionally has no workspace_id label — would create unbounded cardinality.
	URLsShortenedTotal prometheus.Counter

	RateLimitTotal *prometheus.CounterVec

	// ── Infrastructure: PostgreSQL ───────────────────────────────────────────
	// DBPoolConnections exposes connection pool statistics per pool.
	// Updated by a background goroutine every 15 seconds.
	//
	// Labels:
	//   pool:  "primary" | "replica"
	//   state: "total" | "idle" | "acquired" | "max"
	//
	// Pool saturation query (alert if > 0.8 for 5m):
	//   urlshortener_db_pool_connections{state="acquired"}
	//   / urlshortener_db_pool_connections{state="max"}
	DBPoolConnections *prometheus.GaugeVec

	// ── Infrastructure: Redis ────────────────────────────────────────────────
	// CachePoolConnections exposes Redis connection pool statistics.
	// Labels:
	//   state: "total" | "idle" | "stale"
	CachePoolConnections *prometheus.GaugeVec

	// ── Build information ────────────────────────────────────────────────────
	// BuildInfo is a gauge always set to 1, carrying version metadata as labels.
	// This is a standard Prometheus pattern for exposing version information.
	// In Grafana: use a table panel to display it, or join with other metrics
	// to add version context to alerts.
	//
	// Query to get current version: urlshortener_build_info{job="api-service"}
	BuildInfo *prometheus.GaugeVec
}

// New creates and initializes a Metrics registry for the given service.
// Registers:
//   - All platform-specific metrics defined in this struct
//   - Go runtime metrics (goroutines, GC, heap) via GoCollector
//   - OS process metrics (CPU, memory, file descriptors) via ProcessCollector
//
// The serviceName, version, and commit values are attached to BuildInfo
// and used as the "service" label on all HTTP metrics.
func New(serviceName, version, commit string) *Metrics {
	r := prometheus.NewRegistry()

	// Register standard Go and process collectors on our custom registry.
	// These provide goroutine counts, GC stats, memory usage etc.
	// Essential for diagnosing memory leaks and GC pressure.
	r.MustRegister(
		collectors.NewGoCollector(
			collectors.WithGoCollectorRuntimeMetrics(
				collectors.MetricsAll,
			),
		),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	m := &Metrics{
		registry: r,

		HTTPRequestsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "urlshortener",
			Name:      "http_requests_total",
			Help:      "Total number of HTTP requests partitioned by service, method, path, and status code.",
		}, []string{"service", "method", "path", "status_code"}),

		HTTPRequestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "urlshortener",
			Name:      "http_request_duration_seconds",
			Help:      "HTTP request duration in seconds. Use histogram_quantile() to derive P50/P95/P99.",
			Buckets:   httpDurationBuckets,
		}, []string{"service", "method", "path", "status_code"}),

		RedirectsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "urlshortener",
			Name:      "redirects_total",
			Help:      "Total redirect resolutions by cache outcome. Used to compute cache hit ratio (SLI-05).",
		}, []string{"cache_status"}),

		URLsShortenedTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "urlshortener",
			Name:      "urls_shortened_total",
			Help:      "Total number of URLs successfully shortened since process start.",
		}),

		RateLimitTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "urlshortener",
			Name:      "rate_limit_checks_total",
			Help:      "Total rate limit checks by outcome.",
		}, []string{"service", "tier", "class", "result"}),

		DBPoolConnections: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "urlshortener",
			Name:      "db_pool_connections",
			Help:      "PostgreSQL connection pool state. Use acquired/max ratio for saturation alerting.",
		}, []string{"pool", "state"}),

		CachePoolConnections: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "urlshortener",
			Name:      "cache_pool_connections",
			Help:      "Redis connection pool state.",
		}, []string{"state"}),

		BuildInfo: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "urlshortener",
			Name:      "build_info",
			Help:      "Build metadata. Always 1. Use label selectors to filter by version.",
		}, []string{"service", "version", "commit", "go_version"}),
	}

	// Register all platform metrics on the custom registry.
	r.MustRegister(
		m.HTTPRequestsTotal,
		m.HTTPRequestDuration,
		m.RedirectsTotal,
		m.URLsShortenedTotal,
		m.RateLimitTotal,
		m.DBPoolConnections,
		m.CachePoolConnections,
		m.BuildInfo,
	)

	// Set build info — this gauge is always 1 and carries version as labels.
	m.BuildInfo.WithLabelValues(
		serviceName,
		version,
		commit,
		runtime.Version(),
	).Set(1)

	return m
}

// ── Recording methods ─────────────────────────────────────────────────────────
// These methods provide a type-safe API for recording metrics.
// Callers never interact with raw prometheus metric types directly —
// they call these methods, which enforce correct label usage.

// RecordHTTPRequest records a completed HTTP request.
// Called by the metrics middleware after each request completes.
//
// statusCode is converted to string here (not by the caller) to enforce
// consistent label format — "200" not 200 or "HTTP 200".
func (m *Metrics) RecordHTTPRequest(service, method, path string, statusCode int, durationSeconds float64) {
	code := statusCodeToString(statusCode)
	m.HTTPRequestsTotal.WithLabelValues(service, method, path, code).Inc()
	m.HTTPRequestDuration.WithLabelValues(service, method, path, code).Observe(durationSeconds)
}

// RecordRedirect records a redirect resolution with its cache outcome.
// Called by the redirect HTTP handler after the resolve use case returns.
//
// cacheStatus values: "hit" | "miss" | "negative_hit" | "error"
// These must match what resolve.Result.CacheStatus returns.
func (m *Metrics) RecordRedirect(cacheStatus string) {
	m.RedirectsTotal.WithLabelValues(cacheStatus).Inc()
}

// RecordURLShortened increments the URLs shortened counter.
// Called by the shorten HTTP handler on every successful 201 response.
func (m *Metrics) RecordURLShortened() {
	m.URLsShortenedTotal.Inc()
}

// RecordRateLimit records the outcome of a rate limit check.
func (m *Metrics) RecordRateLimit(service, tier, class, result string) {
	m.RateLimitTotal.WithLabelValues(service, tier, class, result).Inc()
}

// UpdateDBPoolStats sets the current connection pool gauge values.
// Called by a background goroutine every 15 seconds (see main.go).
// Using a background goroutine (not per-request) because pool stats
// are stable over short windows — scraping them on every request
// would add unnecessary overhead.
func (m *Metrics) UpdateDBPoolStats(pool string, total, idle, acquired, max int32) {
	m.DBPoolConnections.WithLabelValues(pool, "total").Set(float64(total))
	m.DBPoolConnections.WithLabelValues(pool, "idle").Set(float64(idle))
	m.DBPoolConnections.WithLabelValues(pool, "acquired").Set(float64(acquired))
	m.DBPoolConnections.WithLabelValues(pool, "max").Set(float64(max))
}

// UpdateCachePoolStats sets the current Redis pool gauge values.
func (m *Metrics) UpdateCachePoolStats(total, idle, stale uint32) {
	m.CachePoolConnections.WithLabelValues("total").Set(float64(total))
	m.CachePoolConnections.WithLabelValues("idle").Set(float64(idle))
	m.CachePoolConnections.WithLabelValues("stale").Set(float64(stale))
}

// Handler returns an HTTP handler that serves the /metrics endpoint.
// This handler is mounted on a separate port (default: 9090) so it is
// never exposed through WSO2 or the public Ingress — only Prometheus
// scrapes it directly from the pod's internal network.
//
// EnableOpenMetrics: true allows Prometheus to negotiate the more efficient
// OpenMetrics text format (used by Prometheus 2.x when scraping).
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{
		EnableOpenMetrics: true,
		// ErrorHandling: promhttp.ContinueOnError allows partial scrapes
		// if one collector fails, rather than returning a 500 for the whole scrape.
		ErrorHandling: promhttp.ContinueOnError,
	})
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// statusCodeToString converts an integer HTTP status code to its string
// representation for use as a Prometheus label.
// Using a switch instead of strconv.Itoa provides faster execution and
// prevents allocations on the hot path for common status codes.
func statusCodeToString(code int) string {
	switch code {
	case 200:
		return "200"
	case 201:
		return "201"
	case 204:
		return "204"
	case 301:
		return "301"
	case 302:
		return "302"
	case 400:
		return "400"
	case 401:
		return "401"
	case 403:
		return "403"
	case 404:
		return "404"
	case 409:
		return "409"
	case 410:
		return "410"
	case 422:
		return "422"
	case 429:
		return "429"
	case 500:
		return "500"
	case 503:
		return "503"
	default:
		// Fallback for uncommon codes — avoids a strconv import
		// on the hot path for the common cases above.
		return "other"
	}
}
