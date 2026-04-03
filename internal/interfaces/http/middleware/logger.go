package middleware

import (
	"log/slog"
	"net/http"
	"time"

	chimiddleware "github.com/go-chi/chi/v5/middleware"

	"github.com/urlshortener/platform/pkg/logger"
)

// RequestLogger returns a chi-compatible middleware that:
//  1. Creates a request-scoped child logger with HTTP request fields
//  2. Stores the logger in the request context for use by handlers
//  3. Logs request completion with status code, latency, and bytes written
//
// Log-per-request vs log-in-handler:
//
//	Handlers call logger.FromContext(ctx) to get the request-scoped logger.
//	This pattern ensures every log line from a handler automatically includes
//	the request_id, method, and path — without the handler needing to know
//	about HTTP context. The handler only cares about business logic.
//
// Why log at completion, not at start?
//
//	Logging at completion gives us the status code and latency in a single
//	log line. Log aggregators (Loki, Elasticsearch) can then query for
//	"all 5xx requests that took > 100ms" with a single filter.
//	Logging at start AND completion creates duplicate log entries without
//	proportionally more information.
func RequestLogger(base *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			// Build a request-scoped logger enriched with HTTP request fields.
			// GetReqID reads the X-Request-ID header or generated ID from chi's
			// RequestID middleware (must run before this middleware in the chain).
			reqID := chimiddleware.GetReqID(r.Context())

			reqLog := base.With(
				slog.String("request_id", reqID),
				slog.String("http.method", r.Method),
				slog.String("http.path", r.URL.Path),
				slog.String("http.remote_addr", r.RemoteAddr),
				slog.String("http.user_agent", r.UserAgent()),
			)

			// Store the enriched logger in context.
			// All downstream handlers retrieve it with logger.FromContext(ctx).
			ctx := logger.WithContext(r.Context(), reqLog)

			// Wrap the response writer to capture the status code.
			rw := newStatusResponseWriter(w)

			// Call the next handler in the chain.
			next.ServeHTTP(rw, r.WithContext(ctx))

			// Log the completed request.
			// Status code is now available because the handler has written the response.
			latency := time.Since(start)

			logFn := reqLog.Info
			if rw.Status() >= 500 {
				logFn = reqLog.Error
			} else if rw.Status() >= 400 {
				logFn = reqLog.Warn
			}

			logFn("request completed",
				slog.Int("http.status_code", rw.Status()),
				slog.Duration("http.latency", latency),
				slog.Float64("http.latency_ms", float64(latency.Nanoseconds())/1e6),
			)
		})
	}
}
