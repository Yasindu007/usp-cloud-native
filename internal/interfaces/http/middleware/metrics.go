package middleware

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/urlshortener/platform/internal/infrastructure/metrics"
)

// Metrics returns a chi-compatible middleware that records HTTP request
// metrics for every request processed by the handler chain.
//
// What it records per request:
//   - Request count: service × method × path × status_code
//   - Request duration: service × method × path × status_code
//
// Path label strategy (cardinality control):
//
//	We use the chi route pattern, NOT the raw request URL.
//	Example: "GET /{shortcode}" — not "GET /abc1234"
//	If we used the raw URL, every unique short code creates a new label
//	combination → millions of Prometheus time series → OOM.
//	Chi's route context has the pattern after routing completes.
//	For unmatched routes (404s), we use "unmatched" to prevent
//	attacker-controlled label values from exploding cardinality.
//
// Middleware position in chain:
//
//	Must be placed BEFORE chi.Recoverer so that panics caught by
//	Recoverer (which write 500) are still counted by this middleware.
//	The defer pattern below ensures metrics are recorded even when
//	the inner handler panics.
//
//	Correct order: RequestID → RealIP → OTel → Logger → Metrics → Recoverer → Handler
func Metrics(m *metrics.Metrics, serviceName string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			// Wrap the response writer to capture the status code.
			rw := newStatusResponseWriter(w)

			// Call the rest of the handler chain.
			next.ServeHTTP(rw, r)

			// Record metrics after the handler returns.
			// This executes even if the handler panicked and Recoverer wrote 500,
			// because Recoverer is INSIDE this middleware and catches the panic
			// before it propagates here.
			duration := time.Since(start).Seconds()

			// Get the matched route pattern from chi's routing context.
			// This is safe to call here because chi has already matched
			// the route before executing the middleware chain.
			path := routePattern(r)

			m.RecordHTTPRequest(serviceName, r.Method, path, rw.Status(), duration)
		})
	}
}

// routePattern extracts the chi route pattern from the request context.
// Falls back to "unmatched" for 404s (no matching route) and to the
// raw path for requests outside chi routing (should not happen in practice).
//
// Why "unmatched" instead of the actual path for 404s?
//
//	An attacker can send requests to arbitrary paths:
//	GET /../../../../etc/passwd
//	GET /a, GET /b, GET /c, ... (1M unique paths)
//	If we used the raw path as a label, each creates a new Prometheus time series.
//	At scale this causes: OOM in Prometheus, scrape timeouts, label explosion.
//	"unmatched" collapses all 404 traffic into one label value.
func routePattern(r *http.Request) string {
	rctx := chi.RouteContext(r.Context())
	if rctx == nil {
		return r.URL.Path
	}
	pattern := rctx.RoutePattern()
	if pattern == "" {
		return "unmatched"
	}
	return pattern
}
