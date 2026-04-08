package url

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/urlshortener/platform/internal/application/apperrors"
	domainurl "github.com/urlshortener/platform/internal/domain/url"
)

// UpdateCommand carries inputs for the UpdateURL use case.
//
// PATCH semantics:
//
//	Only non-nil fields are updated. Fields that are nil are left unchanged.
//	This implements the PATCH (partial update) pattern from the PRD.
//
//	We use pointer fields for optional values so that callers can
//	distinguish "not provided" (nil) from "explicitly set to empty string".
//	Without pointers: an empty OriginalURL could mean "clear the URL"
//	or "not provided by the caller" — ambiguous and error-prone.
type UpdateCommand struct {
	URLID       string
	WorkspaceID string

	// OriginalURL is the new target URL. nil = no change.
	OriginalURL *string

	// Title is the new human-readable label. nil = no change.
	Title *string

	// ExpiresAt is the new expiry time. nil = no change.
	// To remove expiry, pass a pointer to a zero time.Time.
	ExpiresAt **time.Time // double pointer: nil = no change, *nil = remove expiry
}

// UpdateHandler orchestrates the UpdateURL use case.
//
// Cache invalidation is a critical responsibility:
//
//	When a URL's original_url is changed, any cached redirect for that
//	short code would serve the OLD target until the cache TTL expires.
//	The update handler MUST invalidate the cache after a successful DB update.
//
//	Cache invalidation failure is non-fatal: the update succeeds but
//	the cache may serve stale data for up to TTL seconds. We log the
//	failure so the Prometheus alert fires for persistent cache errors.
type UpdateHandler struct {
	repo    domainurl.Repository
	cache   domainurl.CachePort
	baseURL string
	log     *slog.Logger
}

// NewUpdateHandler creates an UpdateHandler.
func NewUpdateHandler(
	repo domainurl.Repository,
	cache domainurl.CachePort,
	baseURL string,
	log *slog.Logger,
) *UpdateHandler {
	return &UpdateHandler{repo: repo, cache: cache, baseURL: baseURL, log: log}
}

// Handle executes the UpdateURL use case.
func (h *UpdateHandler) Handle(ctx context.Context, cmd UpdateCommand) (*URLResult, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "UpdateURL.Handle",
		trace.WithAttributes(
			attribute.String("url.id", cmd.URLID),
			attribute.String("workspace.id", cmd.WorkspaceID),
		),
	)
	defer span.End()

	// ── Fetch current state ───────────────────────────────────────────────────
	// We read the current record before applying changes.
	// This is the "read-modify-write" pattern for PATCH operations.
	// We use GetByID (workspace-scoped) to prevent cross-workspace updates.
	u, err := h.repo.GetByID(ctx, cmd.URLID, cmd.WorkspaceID)
	if err != nil {
		if domainurl.IsNotFound(err) {
			return nil, apperrors.ErrNotFound
		}
		span.RecordError(err)
		return nil, fmt.Errorf("fetching url for update: %w", err)
	}

	// ── Apply partial update ──────────────────────────────────────────────────
	// Only mutate fields where the command provides a non-nil value.
	if cmd.OriginalURL != nil {
		if err := validateUpdateURL(*cmd.OriginalURL); err != nil {
			return nil, err
		}
		u.OriginalURL = *cmd.OriginalURL
	}
	if cmd.Title != nil {
		u.Title = *cmd.Title
	}
	if cmd.ExpiresAt != nil {
		// Double pointer: *cmd.ExpiresAt is either nil (remove expiry) or a time
		u.ExpiresAt = *cmd.ExpiresAt
	}

	// UpdatedAt is set by the DB trigger — we set it here too so the
	// returned result reflects the actual update time without a re-fetch.
	u.UpdatedAt = time.Now().UTC()

	// ── Persist ───────────────────────────────────────────────────────────────
	if err := h.repo.Update(ctx, u); err != nil {
		if domainurl.IsNotFound(err) {
			return nil, apperrors.ErrNotFound
		}
		span.RecordError(err)
		return nil, fmt.Errorf("updating url: %w", err)
	}

	// ── Invalidate cache ──────────────────────────────────────────────────────
	// Must happen AFTER a successful DB update.
	// If cache invalidation fails, the update still succeeded —
	// the stale cache entry will expire at its TTL.
	if h.cache != nil {
		if err := h.cache.Delete(ctx, u.ShortCode); err != nil {
			h.log.Warn("failed to invalidate cache after url update",
				slog.String("short_code", u.ShortCode),
				slog.String("error", err.Error()),
			)
			// Non-fatal: log and continue
		}
	}

	return toURLResult(u, h.baseURL), nil
}

// validateUpdateURL validates the new URL value for an update operation.
// Applies the same validation rules as the shorten use case.
func validateUpdateURL(rawURL string) error {
	if rawURL == "" {
		return apperrors.NewValidationError("original_url cannot be empty", nil)
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
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
	default:
		return apperrors.NewValidationError(
			fmt.Sprintf("original_url scheme %q is not allowed", parsed.Scheme),
			nil,
		)
	}
	return nil
}
