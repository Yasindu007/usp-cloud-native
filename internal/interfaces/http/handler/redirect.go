package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"

	appanalytics "github.com/urlshortener/platform/internal/application/analytics"
	"github.com/urlshortener/platform/internal/application/apperrors"
	"github.com/urlshortener/platform/internal/application/resolve"
	"github.com/urlshortener/platform/internal/infrastructure/metrics"
	"github.com/urlshortener/platform/internal/interfaces/http/response"
	"github.com/urlshortener/platform/pkg/logger"
)

// URLResolver is the application use case interface for resolving short codes.
type URLResolver interface {
	Handle(ctx context.Context, q resolve.Query) (*resolve.Result, error)
}

// AnalyticsCapturer is the interface for capturing redirect analytics events.
// Defined here at the consumer boundary (not in the application package).
type AnalyticsCapturer interface {
	Capture(req appanalytics.CaptureRequest)
}

// RedirectHandler handles GET /{shortcode}.
// Story 3.1: now accepts an optional AnalyticsCapturer to record redirect events.
type RedirectHandler struct {
	resolver  URLResolver
	analytics AnalyticsCapturer // nil-safe: analytics are optional
	metrics   *metrics.Metrics  // nil-safe
	log       *slog.Logger
}

// NewRedirectHandler constructs a RedirectHandler.
// analytics and metrics are variadic for backwards compatibility with existing tests.
func NewRedirectHandler(
	resolver URLResolver,
	log *slog.Logger,
	analytics AnalyticsCapturer,
	m ...*metrics.Metrics,
) *RedirectHandler {
	var met *metrics.Metrics
	if len(m) > 0 {
		met = m[0]
	}
	return &RedirectHandler{
		resolver:  resolver,
		analytics: analytics,
		metrics:   met,
		log:       log,
	}
}

// Handle processes GET /{shortcode}.
//
// Analytics capture (Story 3.1):
//
//	After the redirect response is written, a CaptureRequest is sent to the
//	analytics service. This is a non-blocking channel send — the response
//	is fully written before the analytics call. The analytics service handles
//	enrichment (UA parsing, IP hashing, GeoIP) and batch persistence.
//
// Why capture AFTER writing the response?
//
//	The redirect response (HTTP 302) must be sent as fast as possible.
//	Analytics enrichment (~800ns) and the channel send (~100ns) add <1ms.
//	But conceptually, analytics captures "the redirect happened" — so it
//	should be called after the redirect is confirmed, not before.
func (h *RedirectHandler) Handle(w http.ResponseWriter, r *http.Request) {
	log := logger.FromContext(r.Context()).With(
		slog.String("handler", "RedirectHandler"),
		slog.String("request_id", chimiddleware.GetReqID(r.Context())),
	)

	shortCode := chi.URLParam(r, "shortcode")
	if shortCode == "" {
		response.NotFound(w, r.URL.Path)
		return
	}

	log = log.With(slog.String("short_code", shortCode))

	q := resolve.Query{
		ShortCode: shortCode,
		RequestMetadata: resolve.RequestMetadata{
			IPAddress: r.RemoteAddr,
			UserAgent: r.UserAgent(),
			Referrer:  r.Referer(),
			RequestID: chimiddleware.GetReqID(r.Context()),
		},
	}

	result, err := h.resolver.Handle(r.Context(), q)
	if err != nil {
		h.writeError(w, r, err, shortCode, log)
		return
	}

	// Record Prometheus redirect metric (cache hit/miss/negative_hit)
	if h.metrics != nil {
		h.metrics.RecordRedirect(result.CacheStatus)
	}

	// Write the redirect response before analytics capture.
	w.Header().Set("Cache-Control", "no-store")

	log.Info("redirect served",
		slog.String("cache_status", result.CacheStatus),
	)

	http.Redirect(w, r, result.OriginalURL, http.StatusFound)

	// ── Analytics capture (after response is written) ─────────────────────────
	// Capture is called after http.Redirect writes the response. The channel
	// send is non-blocking — if the channel is full, the event is dropped and
	// the dropped counter is incremented. This NEVER delays the redirect.
	if h.analytics != nil {
		h.analytics.Capture(appanalytics.CaptureRequest{
			ShortCode:    result.ShortCode,
			WorkspaceID:  result.WorkspaceID,
			RawIP:        r.RemoteAddr,
			UserAgentRaw: r.UserAgent(),
			ReferrerRaw:  r.Referer(),
			RequestID:    chimiddleware.GetReqID(r.Context()),
			OccurredAt:   time.Now().UTC(),
		})
	}
}

func (h *RedirectHandler) writeError(
	w http.ResponseWriter,
	r *http.Request,
	err error,
	shortCode string,
	log *slog.Logger,
) {
	if errors.Is(err, apperrors.ErrNotFound) {
		log.Debug("short code not found")
		response.NotFound(w, r.URL.Path)
		return
	}
	if errors.Is(err, apperrors.ErrURLDisabled) {
		log.Debug("short code disabled")
		response.NotFound(w, r.URL.Path)
		return
	}
	if errors.Is(err, apperrors.ErrURLExpired) {
		log.Debug("short code expired")
		response.Gone(w, r.URL.Path)
		return
	}
	log.Error("unexpected error resolving short code",
		slog.String("short_code", shortCode),
		slog.String("error", err.Error()),
	)
	response.InternalError(w, r.URL.Path)
}
