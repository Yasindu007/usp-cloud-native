// Package resolve contains the ResolveURL use case.
// Updated in Story 3.1 to integrate with the analytics event capture pipeline.
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
type Handler struct {
	repo        domainurl.ReadonlyRepository
	cache       domainurl.CachePort
	cacheTTL    int
	negativeTTL int
	log         *slog.Logger
}

// NewHandler creates a fully configured ResolveURL handler.
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
// Story 3.1 change: Result now includes WorkspaceID for analytics attribution.
func (h *Handler) Handle(ctx context.Context, q Query) (*Result, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "ResolveURL.Handle",
		trace.WithAttributes(
			attribute.String("url.short_code", q.ShortCode),
		),
	)
	defer span.End()

	// ── Step 1: Positive cache lookup ─────────────────────────────────────────
	if h.cache != nil {
		cached, err := h.cache.Get(ctx, q.ShortCode)
		if err != nil {
			h.log.Warn("cache GET failed, falling through to database",
				slog.String("short_code", q.ShortCode),
				slog.String("error", err.Error()),
			)
		} else if cached != nil {
			span.SetAttributes(attribute.String("cache.status", "hit"))
			if !cached.CanRedirect() {
				return h.handleNonRedirectableURL(ctx, cached, q.ShortCode)
			}
			// NOTE: click counting is now handled by the analytics service
			// in the redirect HTTP handler — not here. The resolve handler
			// is pure resolution logic only.
			return &Result{
				OriginalURL: cached.OriginalURL,
				ShortCode:   q.ShortCode,
				WorkspaceID: cached.WorkspaceID,
				CacheStatus: "hit",
			}, nil
		}
	}

	// ── Step 2: Negative cache lookup ─────────────────────────────────────────
	if h.cache != nil {
		isNegative, err := h.cache.IsNotFound(ctx, q.ShortCode)
		if err != nil {
			h.log.Warn("negative cache lookup failed",
				slog.String("short_code", q.ShortCode),
				slog.String("error", err.Error()),
			)
		} else if isNegative {
			span.SetAttributes(attribute.String("cache.status", "negative_hit"))
			return nil, apperrors.ErrNotFound
		}
	}

	// ── Step 3: Database lookup ────────────────────────────────────────────────
	span.SetAttributes(attribute.String("cache.status", "miss"))

	u, err := h.repo.GetByShortCode(ctx, q.ShortCode)
	if err != nil {
		if domainurl.IsNotFound(err) {
			h.asyncSetNotFound(ctx, q.ShortCode)
			return nil, apperrors.ErrNotFound
		}
		span.RecordError(err)
		return nil, fmt.Errorf("resolving short code %q: %w", q.ShortCode, err)
	}

	if !u.CanRedirect() {
		h.asyncSetNotFound(ctx, q.ShortCode)
		return h.handleNonRedirectableURL(ctx, u, q.ShortCode)
	}

	h.asyncSetCache(ctx, u)

	return &Result{
		OriginalURL: u.OriginalURL,
		ShortCode:   q.ShortCode,
		WorkspaceID: u.WorkspaceID,
		CacheStatus: "miss",
	}, nil
}

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
		h.log.Warn("url in unexpected non-redirectable state",
			slog.String("short_code", shortCode),
			slog.String("status", string(u.Status)),
		)
		return nil, apperrors.ErrNotFound
	}
}

func (h *Handler) asyncSetCache(ctx context.Context, u *domainurl.URL) {
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
