// Package logger provides a structured, context-aware logger built on
// the standard library's log/slog package (available since Go 1.21).
//
// Design decisions:
//   - Uses log/slog (stdlib) to avoid an external dependency
//   - JSON format in production for machine parsing (Loki, Elasticsearch)
//   - Text format in development for human readability
//   - Context integration for automatic trace ID propagation
//   - Logger-per-request pattern: handlers create child loggers with
//     request-scoped fields (request_id, user_id, workspace_id)
//
// Tradeoff vs zap:
//   zap offers ~3x lower allocation rate and better performance for
//   very high-throughput services. For the redirect hot path at 10k RPS,
//   we will profile in Phase 4 and migrate if log allocation shows up
//   in pprof. For now, slog's zero-dependency simplicity wins.
package logger

import (
	"context"
	"log/slog"
	"os"
	"strings"
)

// contextKey is an unexported type for context keys in this package.
// Using a named type prevents collisions with keys from other packages.
type contextKey string

const loggerContextKey contextKey = "logger"

// New creates and returns a configured *slog.Logger.
//
//	level:  "debug" | "info" | "warn" | "error"
//	format: "json" | "text"
//
// The returned logger includes no default fields — callers add
// service-specific fields using logger.With(...).
func New(level string, format string) *slog.Logger {
	var l slog.Level
	switch strings.ToLower(level) {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{
		Level: l,
		// AddSource adds file:line to every log entry.
		// Useful during debugging; disable in production for performance.
		AddSource: l == slog.LevelDebug,
	}

	var handler slog.Handler
	if strings.ToLower(format) == "text" {
		handler = slog.NewTextHandler(os.Stdout, opts)
	} else {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	}

	return slog.New(handler)
}

// WithContext stores a logger in a context.Context.
// Used by middleware to attach a request-scoped logger (with request_id,
// trace_id, user_id) that handlers can retrieve.
func WithContext(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerContextKey, l)
}

// FromContext retrieves the logger stored in a context.
// Falls back to the default slog logger if none is present.
// This means code can always call logger.FromContext(ctx) safely.
func FromContext(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(loggerContextKey).(*slog.Logger); ok && l != nil {
		return l
	}
	return slog.Default()
}

// WithRequestID returns a child logger with the request_id field set.
// Called in the request ID middleware so all downstream log entries
// include the request ID for correlation.
func WithRequestID(l *slog.Logger, requestID string) *slog.Logger {
	return l.With(slog.String("request_id", requestID))
}

// WithTraceContext returns a child logger enriched with OTel trace context.
// Called in the OTel middleware after extracting trace/span IDs.
// This links structured logs to distributed traces in Grafana/Jaeger.
func WithTraceContext(l *slog.Logger, traceID, spanID string) *slog.Logger {
	if traceID == "" && spanID == "" {
		return l
	}
	return l.With(
		slog.String("trace_id", traceID),
		slog.String("span_id", spanID),
	)
}

// WithUserContext returns a child logger with user and workspace fields.
// Called after JWT validation middleware sets user context.
func WithUserContext(l *slog.Logger, userID, workspaceID string) *slog.Logger {
	return l.With(
		slog.String("user_id", userID),
		slog.String("workspace_id", workspaceID),
	)
}