package url

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

// DeleteCommand carries inputs for the DeleteURL use case.
// PRD section 5.1.6: soft-delete only.
// Hard deletion is reserved for the 90-day purge job (Phase 3).
type DeleteCommand struct {
	URLID       string
	WorkspaceID string
}

// DeleteHandler orchestrates the DeleteURL use case.
//
// Soft-delete semantics:
//
//	The URL row is NOT removed from the database.
//	deleted_at is set to now(), status is set to "deleted".
//	Subsequent redirect requests return HTTP 404.
//	The record is retained for 90 days for audit compliance (PRD 5.1.6).
//
// Cache invalidation is critical:
//
//	After soft-delete, the redirect service must not serve the old target.
//	We delete the Redis cache entry immediately.
//	If cache invalidation fails, the next redirect for this code will
//	hit PostgreSQL (GetByShortCode) and return ErrDeleted → 404.
//	The Redis entry will also expire at its TTL.
//	Correctness is maintained even on cache invalidation failure.
type DeleteHandler struct {
	repo  domainurl.Repository
	cache domainurl.CachePort
	log   *slog.Logger
}

// NewDeleteHandler creates a DeleteHandler.
func NewDeleteHandler(
	repo domainurl.Repository,
	cache domainurl.CachePort,
	log *slog.Logger,
) *DeleteHandler {
	return &DeleteHandler{repo: repo, cache: cache, log: log}
}

// Handle executes the DeleteURL use case.
// Returns nil on success (HTTP handler returns 204 No Content).
func (h *DeleteHandler) Handle(ctx context.Context, cmd DeleteCommand) error {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "DeleteURL.Handle",
		trace.WithAttributes(
			attribute.String("url.id", cmd.URLID),
			attribute.String("workspace.id", cmd.WorkspaceID),
		),
	)
	defer span.End()

	// We need the short code for cache invalidation but SoftDelete only
	// takes the ID. Fetch first to get the short code.
	// This is a read-before-delete — acceptable because:
	// 1. The row exists (we just fetched it)
	// 2. The delete is scoped to the workspace (both fetch and delete use workspace_id)
	// 3. Concurrent deletes are safe (soft-delete is idempotent via deleted_at IS NULL guard)
	u, err := h.repo.GetByID(ctx, cmd.URLID, cmd.WorkspaceID)
	if err != nil {
		if domainurl.IsNotFound(err) {
			return apperrors.ErrNotFound
		}
		span.RecordError(err)
		return fmt.Errorf("fetching url for delete: %w", err)
	}

	shortCode := u.ShortCode

	// ── Soft delete ───────────────────────────────────────────────────────────
	if err := h.repo.SoftDelete(ctx, cmd.URLID, cmd.WorkspaceID); err != nil {
		if domainurl.IsNotFound(err) {
			return apperrors.ErrNotFound
		}
		span.RecordError(err)
		return fmt.Errorf("soft deleting url: %w", err)
	}

	// ── Invalidate cache ──────────────────────────────────────────────────────
	if h.cache != nil {
		if err := h.cache.Delete(ctx, shortCode); err != nil {
			h.log.Warn("failed to invalidate cache after url delete",
				slog.String("short_code", shortCode),
				slog.String("error", err.Error()),
			)
			// Non-fatal: redirect service falls through to DB which returns ErrDeleted → 404
		}
	}

	h.log.Info("url soft-deleted",
		slog.String("id", cmd.URLID),
		slog.String("short_code", shortCode),
		slog.String("workspace_id", cmd.WorkspaceID),
	)

	return nil
}
