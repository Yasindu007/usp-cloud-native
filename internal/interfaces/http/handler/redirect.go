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
	"github.com/urlshortener/platform/internal/interfaces/http/response"
	"github.com/urlshortener/platform/pkg/logger"
)

// URLResolver is the interface this handler uses to invoke the resolve use case.
type URLResolver interface {
	Handle(ctx context.Context, q resolve.Query) (*resolve.Result, error)
}

// RedirectHandler handles GET /{shortcode} requests.
// This is the critical hot path — every redirect hits this handler.
// It must be as lean as possible: no unnecessary allocations, no
// blocking I/O on the critical path.
type RedirectHandler struct {
	resolver URLResolver
	log      *slog.Logger
}

// NewRedirectHandler constructs a RedirectHandler.
func NewRedirectHandler(resolver URLResolver, log *slog.Logger) *RedirectHandler {
	return &RedirectHandler{
		resolver: resolver,
		log:      log,
	}
}

// Handle processes GET /{shortcode}.
//
// Resolution flow:
//  1. Extract shortcode from URL parameter (chi routing)
//  2. Build RequestMetadata from HTTP headers (for analytics, Phase 3)
//  3. Invoke the resolve use case
//  4. On success:  HTTP 302 Found + Location header
//  5. On expired:  HTTP 410 Gone  + Problem Details body
//  6. On missing:  HTTP 404 Not Found + Problem Details body
//  7. On error:    HTTP 500 Internal Server Error + Problem Details body
//
// Why HTTP 302 (Found) and not 301 (Moved Permanently)?
//
//	301 is cached permanently by browsers. If the URL target changes
//	(Update operation), browsers would continue redirecting to the old URL
//	indefinitely, breaking the update feature. 302 is the correct default.
//	Users can opt into 301 behaviour for specific URLs in a future version.
//
// Security: no original URL is returned in error responses.
// The shortcode does not reveal what it points to until the redirect succeeds.
func (h *RedirectHandler) Handle(w http.ResponseWriter, r *http.Request) {
	log := logger.FromContext(r.Context()).With(
		slog.String("handler", "RedirectHandler"),
		slog.String("request_id", chimiddleware.GetReqID(r.Context())),
	)

	// ── Extract and validate short code ──────────────────────────────────────
	shortCode := chi.URLParam(r, "shortcode")
	if shortCode == "" {
		// chi routes guarantee {shortcode} is set if the route matched.
		// This branch is a defensive guard against misconfigured routing.
		response.NotFound(w, r.URL.Path)
		return
	}

	log = log.With(slog.String("short_code", shortCode))

	// ── Build query ───────────────────────────────────────────────────────────
	// RequestMetadata is populated for Phase 3 analytics event capture.
	// In Phase 1, the resolve handler ignores it. The data is collected
	// now so Phase 3 is a zero-change addition to this handler.
	q := resolve.Query{
		ShortCode: shortCode,
		RequestMetadata: resolve.RequestMetadata{
			IPAddress: r.RemoteAddr,
			UserAgent: r.UserAgent(),
			Referrer:  r.Referer(),
			RequestID: chimiddleware.GetReqID(r.Context()),
		},
	}

	// ── Execute resolve use case ──────────────────────────────────────────────
	result, err := h.resolver.Handle(r.Context(), q)
	if err != nil {
		h.writeError(w, r, err, shortCode, log)
		return
	}

	// ── Write redirect response ───────────────────────────────────────────────
	// http.Redirect sets the Location header and writes the status code.
	// We use 302 (temporary redirect) — see design note above.
	//
	// We also set Cache-Control: no-store to prevent intermediary proxies and
	// CDNs from caching the redirect. If a URL's target is updated, we want
	// the next request to hit our service — not a cached redirect.
	//
	// For Phase 4, popular short codes can opt into caching via
	// Cache-Control: max-age=N to offload traffic from the redirect service.
	w.Header().Set("Cache-Control", "no-store")

	log.Info("redirect served",
		slog.String("cache_status", result.CacheStatus),
		slog.String("target", result.OriginalURL),
	)

	http.Redirect(w, r, result.OriginalURL, http.StatusFound)
}

// writeError translates application errors to the correct HTTP response.
//
// Redirect error responses return Problem Details JSON — not an HTML page.
// This is important for API clients that programmatically follow redirects
// and need machine-readable errors when a short code is not found.
func (h *RedirectHandler) writeError(
	w http.ResponseWriter,
	r *http.Request,
	err error,
	shortCode string,
	log *slog.Logger,
) {
	// ── Not found → 404 ───────────────────────────────────────────────────────
	if errors.Is(err, apperrors.ErrNotFound) {
		log.Debug("short code not found", slog.String("short_code", shortCode))
		response.NotFound(w, r.URL.Path)
		return
	}

	// ── Disabled → 404 ────────────────────────────────────────────────────────
	// We return 404 (not 403 Forbidden) for disabled URLs to avoid
	// revealing that the resource exists. Information leakage about URL
	// existence could be used to enumerate workspace content.
	if errors.Is(err, apperrors.ErrURLDisabled) {
		log.Debug("short code disabled", slog.String("short_code", shortCode))
		response.NotFound(w, r.URL.Path)
		return
	}

	// ── Expired → 410 Gone ────────────────────────────────────────────────────
	// HTTP 410 is the correct status for a resource that existed but is
	// permanently gone (per RFC 7231). It signals to search engines and
	// link checkers that the resource will not return — unlike 404 which
	// implies "might exist elsewhere or in the future."
	if errors.Is(err, apperrors.ErrURLExpired) {
		log.Debug("short code expired", slog.String("short_code", shortCode))
		response.Gone(w, r.URL.Path)
		return
	}

	// ── Unexpected error → 500 ────────────────────────────────────────────────
	log.Error("unexpected error resolving short code",
		slog.String("short_code", shortCode),
		slog.String("error", err.Error()),
	)
	response.InternalError(w, r.URL.Path)
}
