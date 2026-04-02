package shorten

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/urlshortener/platform/internal/application/apperrors"
	domainurl "github.com/urlshortener/platform/internal/domain/url"
	"github.com/urlshortener/platform/pkg/shortcode"
)

const tracerName = "github.com/urlshortener/platform/internal/application/shorten"

// maxRetries is the maximum number of short code generation attempts
// before giving up. Each attempt generates a new random code and
// retries the INSERT. Collision probability at 7 chars Base62:
//
//	P(collision) ≈ N / 62^7 where N = existing URLs
//	At 10M URLs: P ≈ 0.00028% per attempt
//
// 3 retries provides 6σ collision safety at realistic scale.
const maxRetries = 3

// Handler orchestrates the ShortenURL use case.
// It coordinates the domain entity, repository, cache, and short code
// generator to create a new shortened URL.
//
// Dependencies are expressed as interfaces (ports), not concrete types.
// This is the Dependency Inversion Principle in practice:
//   - Handler depends on domainurl.Repository (interface)
//   - Handler depends on domainurl.CachePort (interface)
//   - Handler does NOT depend on postgres.URLRepository (concrete)
//   - Handler does NOT depend on redis.URLCache (concrete)
//
// This means we can test the handler with in-memory fakes (see handler_test.go)
// without starting PostgreSQL or Redis.
type Handler struct {
	repo      domainurl.Repository
	cache     domainurl.CachePort
	generator *shortcode.Generator
	baseURL   string
	cacheTTL  int
	log       *slog.Logger
}

// NewHandler creates a fully configured ShortenURL handler.
//
// Parameters:
//
//	repo      — write-capable URL repository (postgres adapter)
//	cache     — URL cache (redis adapter)
//	generator — short code generator (Base62, crypto/rand)
//	baseURL   — public-facing base URL (e.g., "https://s.example.com")
//	cacheTTL  — TTL in seconds for newly created cache entries
//	log       — structured logger
func NewHandler(
	repo domainurl.Repository,
	cache domainurl.CachePort,
	generator *shortcode.Generator,
	baseURL string,
	cacheTTL int,
	log *slog.Logger,
) *Handler {
	return &Handler{
		repo:      repo,
		cache:     cache,
		generator: generator,
		baseURL:   strings.TrimRight(baseURL, "/"),
		cacheTTL:  cacheTTL,
		log:       log,
	}
}

// Handle executes the ShortenURL use case.
//
// Execution flow:
//  1. Validate the original URL (scheme, format, blocklist)
//  2. Determine short code (custom or generated)
//  3. Build the URL domain entity
//  4. Persist with collision retry (up to maxRetries)
//  5. Pre-warm the redirect cache
//  6. Return the result
//
// Failure modes:
//   - Invalid URL          → apperrors.ErrValidation
//   - Blocked URL          → apperrors.ErrURLBlocked
//   - Short code reserved  → apperrors.ErrValidation
//   - Short code conflict  → apperrors.ErrConflict (after maxRetries)
//   - DB unavailable       → wrapped infrastructure error
func (h *Handler) Handle(ctx context.Context, cmd Command) (*Result, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "ShortenURL.Handle",
		trace.WithAttributes(
			attribute.String("workspace.id", cmd.WorkspaceID),
			attribute.Bool("custom_code", cmd.CustomCode != ""),
		),
	)
	defer span.End()

	// ── Step 1: Validate OriginalURL ─────────────────────────────────────────
	if err := validateURL(cmd.OriginalURL); err != nil {
		span.RecordError(err)
		return nil, err
	}

	// ── Step 2: Determine short code ────────────────────────────────────────
	var code string
	var err error

	if cmd.CustomCode != "" {
		if err := validateCustomCode(cmd.CustomCode); err != nil {
			span.RecordError(err)
			return nil, err
		}
		code = cmd.CustomCode
	}

	// ── Step 3: Build domain entity ─────────────────────────────────────────
	now := time.Now().UTC()
	id := ulid.Make().String()

	u := &domainurl.URL{
		ID:          id,
		WorkspaceID: cmd.WorkspaceID,
		OriginalURL: cmd.OriginalURL,
		Title:       cmd.Title,
		Status:      domainurl.StatusActive,
		ExpiresAt:   cmd.ExpiresAt,
		CreatedBy:   cmd.CreatedBy,
		CreatedAt:   now,
		UpdatedAt:   now,
		ClickCount:  0,
	}

	// ── Step 4: Persist with collision retry ─────────────────────────────────
	// The retry loop handles two scenarios:
	//   a) Custom code: no retry — if it conflicts, return ErrConflict immediately.
	//   b) Generated code: retry up to maxRetries with a freshly generated code.
	//
	// We do NOT pre-check existence before insert (SELECT + INSERT pattern)
	// because that has a TOCTOU (time-of-check-time-of-use) race condition:
	//   Thread A: SELECT → not found
	//   Thread B: SELECT → not found
	//   Thread A: INSERT → ok
	//   Thread B: INSERT → UNIQUE VIOLATION
	// The INSERT itself is the atomic uniqueness check. The retry handles
	// the (rare) case where the DB enforces what we couldn't pre-check.
	for attempt := 1; attempt <= maxRetries; attempt++ {
		if code == "" {
			// Generated code path: generate fresh code on each attempt.
			code, err = h.generator.Generate()
			if err != nil {
				return nil, fmt.Errorf("generating short code: %w", err)
			}
		}

		u.ShortCode = code

		// Validate the complete entity before attempting persistence.
		if err := u.Validate(); err != nil {
			return nil, apperrors.NewValidationError("url entity validation failed", err)
		}

		span.SetAttributes(
			attribute.String("url.short_code", code),
			attribute.Int("attempt", attempt),
		)

		if err = h.repo.Create(ctx, u); err != nil {
			if domainurl.IsConflict(err) {
				if cmd.CustomCode != "" {
					// Custom code conflict is final — no retry.
					h.log.Warn("custom short code conflict",
						slog.String("short_code", code),
						slog.String("workspace_id", cmd.WorkspaceID),
					)
					return nil, apperrors.ErrShortCodeConflict
				}

				// Generated code collision: try a new code on next iteration.
				h.log.Debug("short code collision, retrying",
					slog.String("short_code", code),
					slog.Int("attempt", attempt),
				)
				code = "" // Reset so next iteration generates a new code
				continue
			}
			// Non-collision DB error — unrecoverable
			span.RecordError(err)
			return nil, fmt.Errorf("persisting url: %w", err)
		}

		// INSERT succeeded — break out of retry loop.
		break
	}

	// All retries exhausted on generated code collision — astronomically rare.
	if err != nil && domainurl.IsConflict(err) {
		return nil, apperrors.ErrShortCodeConflict
	}

	h.log.Info("url shortened",
		slog.String("id", u.ID),
		slog.String("short_code", u.ShortCode),
		slog.String("workspace_id", u.WorkspaceID),
	)

	// ── Step 5: Pre-warm redirect cache ──────────────────────────────────────
	// We write to the cache immediately after a successful DB insert.
	// This means the very first redirect for a newly created URL is also
	// a cache hit — zero cold-start latency for the creator's first share.
	//
	// Cache write failure is non-fatal: the redirect service will fall
	// through to PostgreSQL on the first request and populate the cache then.
	// We log the warning so the Prometheus alert fires if this becomes frequent.
	if h.cache != nil {
		if err := h.cache.Set(ctx, u, h.cacheTTL); err != nil {
			h.log.Warn("failed to pre-warm redirect cache",
				slog.String("short_code", u.ShortCode),
				slog.String("error", err.Error()),
			)
			// Non-fatal: continue and return success
		}
	}

	return &Result{
		ShortURL:    h.baseURL + "/" + u.ShortCode,
		ShortCode:   u.ShortCode,
		ID:          u.ID,
		OriginalURL: u.OriginalURL,
		WorkspaceID: u.WorkspaceID,
		CreatedAt:   u.CreatedAt.Format(time.RFC3339),
	}, nil
}

// ── Validation helpers ────────────────────────────────────────────────────────

// validateURL enforces PRD section FR-URL-01:
//   - Must be a valid RFC 3986 URL
//   - Must use http or https scheme
//   - Must not exceed 8192 characters
//
// This is the application-layer boundary validation. The domain entity
// also validates but we check here to return a clean validation error
// before any domain object is constructed.
func validateURL(rawURL string) error {
	if rawURL == "" {
		return apperrors.NewValidationError("original_url is required", nil)
	}

	if len(rawURL) > domainurl.MaxURLLength {
		return apperrors.NewValidationError(
			fmt.Sprintf("original_url exceeds maximum length of %d characters", domainurl.MaxURLLength),
			nil,
		)
	}

	parsed, err := url.ParseRequestURI(rawURL)
	if err != nil {
		return apperrors.NewValidationError("original_url is not a valid URL", err)
	}

	// Enforce https/http only (PRD FR-URL-01).
	// Blocking ftp://, file://, javascript:, data: schemes prevents
	// protocol-based attacks and ensures redirects are web-safe.
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
		// valid
	default:
		return apperrors.NewValidationError(
			fmt.Sprintf("original_url scheme %q is not allowed: only http and https are permitted", parsed.Scheme),
			nil,
		)
	}

	// Host must be present — rejects bare schemes like "https://"
	if parsed.Host == "" {
		return apperrors.NewValidationError("original_url must include a valid host", nil)
	}

	// Phase 1: URL blocklist check is a stub. In Phase 2 we integrate
	// Google Safe Browsing API or a local blocklist file.
	// The function signature is established here so Phase 2 is a drop-in addition.
	if isBlocked(rawURL) {
		return apperrors.ErrURLBlocked
	}

	return nil
}

// reservedPaths are system-level paths that cannot be used as short codes.
// A short code of "healthz" would intercept the liveness probe endpoint.
// A short code of "api" would intercept the API routing prefix.
var reservedPaths = map[string]bool{
	"healthz":     true,
	"readyz":      true,
	"metrics":     true,
	"api":         true,
	"admin":       true,
	"static":      true,
	"favicon.ico": true,
	"robots.txt":  true,
}

// validateCustomCode enforces PRD section FR-URL-02:
//   - Length: 3–32 characters
//   - Characters: alphanumeric, hyphens, underscores only
//   - Not a reserved system path
//
// We deliberately allow hyphens and underscores for branded codes
// like "product-launch" or "sale_2026".
func validateCustomCode(code string) error {
	if len(code) < domainurl.MinShortCodeLength || len(code) > domainurl.MaxShortCodeLength {
		return apperrors.NewValidationError(
			fmt.Sprintf("custom short code must be between %d and %d characters",
				domainurl.MinShortCodeLength, domainurl.MaxShortCodeLength),
			nil,
		)
	}

	for _, r := range code {
		if !isAlphanumericOrSafe(r) {
			return apperrors.NewValidationError(
				fmt.Sprintf("custom short code contains invalid character %q: only alphanumeric, hyphens, and underscores are allowed", r),
				nil,
			)
		}
	}

	if reservedPaths[strings.ToLower(code)] {
		return apperrors.NewValidationError(
			fmt.Sprintf("short code %q is reserved", code),
			nil,
		)
	}

	return nil
}

// isAlphanumericOrSafe returns true if the rune is safe for use in a short code.
func isAlphanumericOrSafe(r rune) bool {
	return (r >= 'a' && r <= 'z') ||
		(r >= 'A' && r <= 'Z') ||
		(r >= '0' && r <= '9') ||
		r == '-' ||
		r == '_'
}

// isBlocked checks the URL against a blocklist.
// Phase 1: stub that always returns false.
// Phase 2: integrates Google Safe Browsing API lookup.
func isBlocked(_ string) bool {
	return false
}
