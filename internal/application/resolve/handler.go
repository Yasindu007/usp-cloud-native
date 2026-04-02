package resolve

import (
	"context"
	"fmt"
	"log/slog"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/urlshortener/platform/internal/application/apperrors"
	domainurl "github.com/urlshortener/platform/internal/domain/url"
)

const tracerName = "github.com/urlshortener/platform/internal/application/resolve"

// Handler orchestrates the ResolveURL use case.
// It implements the following resolution strategy, optimised for minimum
// latency on the common path (cache hit):
//
//  1. Positive cache lookup (Redis GET)          → ~0.5ms  [most requests]
//  2. Negative cache lookup (Redis EXISTS)       → ~0.5ms  [404 requests]
//  3. PostgreSQL lookup (replica SELECT)         → ~5-15ms [cache miss]
//  4. Cache population (Redis SET, async)        → ~0.5ms  [after DB hit]
//  5. Click counter increment (async goroutine)  → ~0.5ms  [non-blocking]
//
// The critical invariant: steps 1–2 MUST complete before any response is
// sent. Steps 4–5 are fire-and-forget — they happen concurrently with
// writing the HTTP response.
type Handler struct {
	repo        domainurl.ReadonlyRepository
	cache       domainurl.CachePort
	cacheTTL    int // positive cache TTL in seconds
	negativeTTL int // negative cache TTL in seconds
	log         *slog.Logger
}

// NewHandler creates a fully configured ResolveURL handler.
//
// Parameters:
//
//	repo        — read-only URL repository (postgres replica adapter)
//	cache       — URL cache (redis adapter); may be nil (DB-only mode)
//	cacheTTL    — TTL in seconds for positive cache entries
//	negativeTTL — TTL in seconds for negative cache entries
//	log         — structured logger
func NewHandler(
	repo domainurl.ReadonlyRepository,
	cache domainurl.CachePort,
	cacheTTL int,
	negativeTTL int,
	log *slog.Logger,
) *Handler {
	return &Handler{
		repo:        repo,
		cache:       cache,
		cacheTTL:    cacheTTL,
		negativeTTL: negativeTTL,
		log:         log,
	}
}

// Handle executes the ResolveURL use case.
//
// Return values:
//
//	(*Result, nil)           — short code resolved; caller issues HTTP 302
//	(nil, apperrors.ErrNotFound)     — short code not found; caller issues HTTP 404
//	(nil, apperrors.ErrURLExpired)   — URL exists but has expired; caller issues HTTP 410
//	(nil, apperrors.ErrURLDisabled)  — URL disabled by owner; caller issues HTTP 404
//	(nil, err)               — infrastructure error; caller issues HTTP 500
//
// Observability contract:
//
//	Result.CacheStatus is always set so the HTTP handler can increment
//	the appropriate Prometheus counter without importing this package's
//	internal logic.
func (h *Handler) Handle(ctx context.Context, q Query) (*Result, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "ResolveURL.Handle",
		trace.WithAttributes(
			attribute.String("url.short_code", q.ShortCode),
		),
	)
	defer span.End()

	// ── Step 1: Positive cache lookup ────────────────────────────────────────
	if h.cache != nil {
		cached, err := h.cache.Get(ctx, q.ShortCode)
		if err != nil {
			// Cache infrastructure error: log and degrade gracefully.
			// We fall through to the DB rather than returning 500.
			// This is the correct behaviour: Redis down != service down.
			// The SLI will show a latency spike but availability is preserved.
			h.log.Warn("cache GET failed, falling through to database",
				slog.String("short_code", q.ShortCode),
				slog.String("error", err.Error()),
			)
		} else if cached != nil {
			// ── CACHE HIT ────────────────────────────────────────────────────
			span.SetAttributes(attribute.String("cache.status", "hit"))

			// Domain rule check even on cache hit:
			// The cached entry might be stale if a URL was expired/disabled
			// after it was cached. The TTL eventually handles this, but
			// we enforce CanRedirect() for correctness within the TTL window.
			if !cached.CanRedirect() {
				return h.handleNonRedirectableURL(ctx, cached, q.ShortCode)
			}

			// Fire-and-forget: increment click count without blocking redirect.
			h.asyncIncrementClickCount(ctx, q.ShortCode)

			return &Result{
				OriginalURL: cached.OriginalURL,
				ShortCode:   q.ShortCode,
				CacheStatus: "hit",
			}, nil
		}
	}

	// ── Step 2: Negative cache lookup ────────────────────────────────────────
	// Check if we've previously confirmed this short code doesn't exist.
	// Prevents repeated DB queries for non-existent codes.
	if h.cache != nil {
		isNegative, err := h.cache.IsNotFound(ctx, q.ShortCode)
		if err != nil {
			h.log.Warn("negative cache lookup failed",
				slog.String("short_code", q.ShortCode),
				slog.String("error", err.Error()),
			)
		} else if isNegative {
			// ── NEGATIVE CACHE HIT ───────────────────────────────────────────
			span.SetAttributes(attribute.String("cache.status", "negative_hit"))
			return nil, apperrors.ErrNotFound
		}
	}

	// ── Step 3: Database lookup ───────────────────────────────────────────────
	span.SetAttributes(attribute.String("cache.status", "miss"))

	u, err := h.repo.GetByShortCode(ctx, q.ShortCode)
	if err != nil {
		if domainurl.IsNotFound(err) {
			// Short code does not exist — populate negative cache so future
			// requests for this code skip the DB entirely.
			h.asyncSetNotFound(ctx, q.ShortCode)
			return nil, apperrors.ErrNotFound
		}
		// Infrastructure error (DB down, timeout, etc.)
		span.RecordError(err)
		return nil, fmt.Errorf("resolving short code %q: %w", q.ShortCode, err)
	}

	// ── Domain rule enforcement ───────────────────────────────────────────────
	if !u.CanRedirect() {
		// Populate negative cache for expired/disabled URLs to prevent
		// further DB queries until TTL expires.
		h.asyncSetNotFound(ctx, q.ShortCode)
		return h.handleNonRedirectableURL(ctx, u, q.ShortCode)
	}

	// ── Step 4: Populate positive cache (async) ───────────────────────────────
	// We do this in a goroutine so the redirect response is not delayed
	// by the Redis write. The next request will be a cache hit.
	//
	// Why async? Our P99 SLO is 50ms. A Redis SET on a warm key takes ~1ms.
	// Doing it synchronously would add 1ms to every DB-miss redirect.
	// At 10k RPS with 5% miss rate, that's 500 Redis SETs/sec on the
	// critical path. Async keeps the critical path as fast as possible.
	//
	// Risk: if the process crashes between returning the redirect and
	// completing the async SET, the cache is not populated. The next
	// request will be a DB miss again — acceptable consistency trade-off.
	h.asyncSetCache(ctx, u)

	// Fire-and-forget click count increment.
	h.asyncIncrementClickCount(ctx, q.ShortCode)

	return &Result{
		OriginalURL: u.OriginalURL,
		ShortCode:   q.ShortCode,
		CacheStatus: "miss",
	}, nil
}

// handleNonRedirectableURL translates a non-redirectable URL state into
// the appropriate application error for the HTTP handler to act on.
func (h *Handler) handleNonRedirectableURL(
	ctx context.Context,
	u *domainurl.URL,
	shortCode string,
) (*Result, error) {
	switch {
	case u.IsExpired():
		return nil, apperrors.ErrURLExpired
	case u.Status == domainurl.StatusDisabled:
		return nil, apperrors.ErrURLDisabled
	case u.Status == domainurl.StatusDeleted:
		return nil, apperrors.ErrNotFound
	default:
		// Unexpected status — log and return not found for safety.
		h.log.Warn("url in unexpected non-redirectable state",
			slog.String("short_code", shortCode),
			slog.String("status", string(u.Status)),
		)
		return nil, apperrors.ErrNotFound
	}
}

// ── Async helpers ─────────────────────────────────────────────────────────────
// These methods spawn goroutines for non-critical side effects.
// They use a detached context (context.Background()) instead of the
// request context because the request context is cancelled when the
// HTTP response is sent — before the goroutine completes.
//
// This is the correct pattern for fire-and-forget operations.
// The trade-off: these operations are not traced (no parent span).
// In Phase 3, we replace these with a proper async worker queue
// so they are reliable (not dropped on crash) and observable.

func (h *Handler) asyncSetCache(ctx context.Context, u *domainurl.URL) {
	// Propagate trace context so the async span links to the parent request.
	spanCtx := trace.SpanContextFromContext(ctx)

	go func() {
		bgCtx := context.Background()
		if spanCtx.IsValid() {
			bgCtx = trace.ContextWithRemoteSpanContext(bgCtx, spanCtx)
		}

		if h.cache == nil {
			return
		}
		if err := h.cache.Set(bgCtx, u, h.cacheTTL); err != nil {
			h.log.Warn("async cache set failed",
				slog.String("short_code", u.ShortCode),
				slog.String("error", err.Error()),
			)
		}
	}()
}

func (h *Handler) asyncSetNotFound(ctx context.Context, shortCode string) {
	spanCtx := trace.SpanContextFromContext(ctx)

	go func() {
		bgCtx := context.Background()
		if spanCtx.IsValid() {
			bgCtx = trace.ContextWithRemoteSpanContext(bgCtx, spanCtx)
		}

		if h.cache == nil {
			return
		}
		if err := h.cache.SetNotFound(bgCtx, shortCode, h.negativeTTL); err != nil {
			h.log.Warn("async set-not-found failed",
				slog.String("short_code", shortCode),
				slog.String("error", err.Error()),
			)
		}
	}()
}

func (h *Handler) asyncIncrementClickCount(_ context.Context, shortCode string) {
	// Note: click count increment uses a write repository operation.
	// In the redirect service, we only hold a ReadonlyRepository.
	// Phase 3 introduces a dedicated analytics event queue that handles
	// click counting via async batch writes — the correct production pattern.
	// For Phase 1 this is a no-op placeholder.
	_ = shortCode
}
