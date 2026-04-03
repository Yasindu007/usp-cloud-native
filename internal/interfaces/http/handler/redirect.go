package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"

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

// RedirectHandler handles GET /{shortcode}.
type RedirectHandler struct {
	resolver URLResolver
	metrics  *metrics.Metrics // nil-safe
	log      *slog.Logger
}

// NewRedirectHandler constructs a RedirectHandler.
// The metrics parameter is variadic for backward compatibility with tests.
func NewRedirectHandler(resolver URLResolver, log *slog.Logger, m ...*metrics.Metrics) *RedirectHandler {
	var met *metrics.Metrics
	if len(m) > 0 {
		met = m[0]
	}
	return &RedirectHandler{resolver: resolver, metrics: met, log: log}
}

// Handle processes GET /{shortcode}.
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

	// Record redirect business metric with cache status.
	// This is the SLI-05 data point: hit vs miss vs negative_hit.
	// The HTTP metrics middleware records the 302 response independently —
	// this counter gives us the cache dimension the HTTP layer cannot see.
	if h.metrics != nil {
		h.metrics.RecordRedirect(result.CacheStatus)
	}

	w.Header().Set("Cache-Control", "no-store")

	log.Info("redirect served",
		slog.String("cache_status", result.CacheStatus),
		slog.String("target", result.OriginalURL),
	)

	http.Redirect(w, r, result.OriginalURL, http.StatusFound)
}

func (h *RedirectHandler) writeError(
	w http.ResponseWriter, r *http.Request,
	err error, shortCode string, log *slog.Logger,
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
