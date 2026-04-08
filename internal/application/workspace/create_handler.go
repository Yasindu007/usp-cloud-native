// Package workspace contains the application use cases for workspace
// and membership management.
package workspace

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/urlshortener/platform/internal/application/apperrors"
	domainworkspace "github.com/urlshortener/platform/internal/domain/workspace"
)

const tracerName = "github.com/urlshortener/platform/internal/application/workspace"

// CreateCommand carries inputs for the CreateWorkspace use case.
type CreateCommand struct {
	// Name is the human-readable workspace name (required, globally unique).
	Name string

	// Slug is the URL-safe identifier. When empty, generated from Name.
	// When provided, must be lowercase alphanumeric with hyphens.
	Slug string

	// OwnerID is the ULID of the authenticated user creating the workspace.
	// Populated from JWT claims — never from the request body.
	OwnerID string
}

// CreateResult is returned on successful workspace creation.
type CreateResult struct {
	ID        string
	Name      string
	Slug      string
	PlanTier  string
	OwnerID   string
	CreatedAt string // RFC 3339
}

// CreateHandler orchestrates the CreateWorkspace use case.
//
// Responsibilities:
//  1. Validate the command (slug format, name length)
//  2. Generate a slug if not provided
//  3. Enforce the MaxWorkspacesPerUser limit
//  4. Create the workspace and owner membership atomically
//  5. Return the result
//
// The atomic creation (workspace + owner member in one transaction)
// is delegated to the repository — this prevents a workspace existing
// without any owner, which would be an orphaned resource.
type CreateHandler struct {
	repo domainworkspace.Repository
	log  *slog.Logger
}

// NewCreateHandler creates a CreateHandler.
func NewCreateHandler(repo domainworkspace.Repository, log *slog.Logger) *CreateHandler {
	return &CreateHandler{repo: repo, log: log}
}

// Handle executes the CreateWorkspace use case.
func (h *CreateHandler) Handle(ctx context.Context, cmd CreateCommand) (*CreateResult, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "CreateWorkspace.Handle",
		trace.WithAttributes(
			attribute.String("owner.id", cmd.OwnerID),
		),
	)
	defer span.End()

	// ── Validate ──────────────────────────────────────────────────────────────
	if cmd.Name == "" {
		return nil, apperrors.NewValidationError("name is required", nil)
	}
	if len(cmd.Name) > 100 {
		return nil, apperrors.NewValidationError("name must be 100 characters or fewer", nil)
	}
	if cmd.OwnerID == "" {
		return nil, apperrors.NewValidationError("owner_id is required", nil)
	}

	// ── Slug generation ───────────────────────────────────────────────────────
	slug := cmd.Slug
	if slug == "" {
		slug = domainworkspace.GenerateSlug(cmd.Name)
	} else {
		slug = strings.ToLower(slug)
	}

	// ── Enforce workspace limit ───────────────────────────────────────────────
	// Check before creating to give a clear error. There is a TOCTOU race
	// here (between count and insert) but it is acceptable:
	//   - The race window is tiny (milliseconds)
	//   - The DB has no such constraint so over-limit is possible but rare
	//   - Phase 2 billing enforcement will add a stricter check
	count, err := h.repo.CountOwnedByUser(ctx, cmd.OwnerID)
	if err != nil {
		return nil, fmt.Errorf("counting owned workspaces: %w", err)
	}
	if count >= domainworkspace.MaxWorkspacesPerUser {
		return nil, apperrors.NewValidationError(
			fmt.Sprintf("you have reached the maximum of %d workspaces",
				domainworkspace.MaxWorkspacesPerUser),
			nil,
		)
	}

	// ── Build domain entities ─────────────────────────────────────────────────
	now := time.Now().UTC()
	id := ulid.Make().String()

	w := &domainworkspace.Workspace{
		ID:        id,
		Name:      cmd.Name,
		Slug:      slug,
		PlanTier:  "free",
		OwnerID:   cmd.OwnerID,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := w.Validate(); err != nil {
		return nil, apperrors.NewValidationError(err.Error(), err)
	}

	ownerMember := &domainworkspace.Member{
		WorkspaceID: id,
		UserID:      cmd.OwnerID,
		Role:        domainworkspace.RoleOwner,
		JoinedAt:    now,
	}

	// ── Persist (workspace + owner member in one transaction) ─────────────────
	if err := h.repo.Create(ctx, w, ownerMember); err != nil {
		if domainworkspace.IsConflict(err) {
			return nil, apperrors.ErrShortCodeConflict // reuse conflict sentinel
		}
		span.RecordError(err)
		return nil, fmt.Errorf("creating workspace: %w", err)
	}

	h.log.Info("workspace created",
		slog.String("id", w.ID),
		slog.String("slug", w.Slug),
		slog.String("owner_id", w.OwnerID),
	)

	return &CreateResult{
		ID:        w.ID,
		Name:      w.Name,
		Slug:      w.Slug,
		PlanTier:  w.PlanTier,
		OwnerID:   w.OwnerID,
		CreatedAt: w.CreatedAt.Format(time.RFC3339),
	}, nil
}
