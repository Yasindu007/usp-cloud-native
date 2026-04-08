package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	domainworkspace "github.com/urlshortener/platform/internal/domain/workspace"
)

// ── WorkspaceRepository ───────────────────────────────────────────────────────

// WorkspaceRepository implements domain/workspace.Repository and
// domain/workspace.MemberRepository using PostgreSQL via pgx v5.
//
// Note: both interfaces are implemented on the same struct because
// workspace and membership data are always in the same database.
// This avoids an extra indirection while still satisfying the
// interface segregation defined in the domain layer.
type WorkspaceRepository struct {
	db *Client
}

// NewWorkspaceRepository creates a WorkspaceRepository.
func NewWorkspaceRepository(db *Client) *WorkspaceRepository {
	return &WorkspaceRepository{db: db}
}

const workspaceTracerName = "github.com/urlshortener/platform/internal/infrastructure/postgres/workspace"

// ── Workspace operations ──────────────────────────────────────────────────────

// Create persists a new workspace and its owner membership in a single
// database transaction. Either both succeed or neither is committed.
// This enforces the invariant that every workspace has an owner member.
func (r *WorkspaceRepository) Create(
	ctx context.Context,
	w *domainworkspace.Workspace,
	ownerMember *domainworkspace.Member,
) error {
	ctx, span := otel.Tracer(workspaceTracerName).Start(ctx, "WorkspaceRepository.Create",
		trace.WithAttributes(
			attribute.String("workspace.slug", w.Slug),
		),
	)
	defer span.End()

	// Use pgx transaction to ensure atomicity.
	tx, err := r.db.Primary().Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	// Rollback is a no-op if Commit is called first.
	defer func() { _ = tx.Rollback(ctx) }()

	const insertWorkspace = `
		INSERT INTO workspaces (id, name, slug, plan_tier, owner_id, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`

	_, err = tx.Exec(ctx, insertWorkspace,
		w.ID, w.Name, w.Slug, w.PlanTier, w.OwnerID, w.CreatedAt, w.UpdatedAt,
	)
	if err != nil {
		span.RecordError(err)
		return translateWorkspaceError(err)
	}

	const insertMember = `
		INSERT INTO workspace_members (workspace_id, user_id, role, invited_by, joined_at)
		VALUES ($1, $2, $3, $4, $5)`

	_, err = tx.Exec(ctx, insertMember,
		ownerMember.WorkspaceID,
		ownerMember.UserID,
		string(ownerMember.Role),
		nilIfEmpty(ownerMember.InvitedBy),
		ownerMember.JoinedAt,
	)
	if err != nil {
		span.RecordError(err)
		return translateWorkspaceError(err)
	}

	if err := tx.Commit(ctx); err != nil {
		span.RecordError(err)
		return fmt.Errorf("committing workspace creation: %w", err)
	}

	return nil
}

// GetByID retrieves a workspace by ULID.
func (r *WorkspaceRepository) GetByID(ctx context.Context, id string) (*domainworkspace.Workspace, error) {
	ctx, span := otel.Tracer(workspaceTracerName).Start(ctx, "WorkspaceRepository.GetByID",
		trace.WithAttributes(
			attribute.String("workspace.id", id),
		),
	)
	defer span.End()

	const query = `
		SELECT id, name, slug, plan_tier, owner_id, created_at, updated_at
		FROM workspaces WHERE id = $1`

	row := r.db.Replica().QueryRow(ctx, query, id)
	w, err := scanWorkspace(row)
	if err != nil {
		span.RecordError(err)
		return nil, translateWorkspaceError(err)
	}
	return w, nil
}

// GetBySlug retrieves a workspace by slug.
func (r *WorkspaceRepository) GetBySlug(ctx context.Context, slug string) (*domainworkspace.Workspace, error) {
	ctx, span := otel.Tracer(workspaceTracerName).Start(ctx, "WorkspaceRepository.GetBySlug",
		trace.WithAttributes(
			attribute.String("workspace.slug", slug),
		),
	)
	defer span.End()

	const query = `
		SELECT id, name, slug, plan_tier, owner_id, created_at, updated_at
		FROM workspaces WHERE slug = $1`

	row := r.db.Replica().QueryRow(ctx, query, slug)
	w, err := scanWorkspace(row)
	if err != nil {
		span.RecordError(err)
		return nil, translateWorkspaceError(err)
	}
	return w, nil
}

// ListForUser returns all workspaces the user is a member of.
func (r *WorkspaceRepository) ListForUser(ctx context.Context, userID string) ([]*domainworkspace.Workspace, error) {
	_, span := otel.Tracer(workspaceTracerName).Start(ctx, "WorkspaceRepository.ListForUser",
		trace.WithAttributes(
			attribute.String("user.id", userID),
		),
	)
	defer span.End()

	const query = `
		SELECT w.id, w.name, w.slug, w.plan_tier, w.owner_id, w.created_at, w.updated_at
		FROM workspaces w
		INNER JOIN workspace_members wm ON wm.workspace_id = w.id
		WHERE wm.user_id = $1
		ORDER BY w.created_at DESC`

	rows, err := r.db.Replica().Query(ctx, query, userID)
	if err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("querying workspaces for user: %w", err)
	}
	defer rows.Close()

	var workspaces []*domainworkspace.Workspace
	for rows.Next() {
		w, err := scanWorkspace(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning workspace row: %w", err)
		}
		workspaces = append(workspaces, w)
	}

	return workspaces, rows.Err()
}

// CountOwnedByUser counts workspaces owned by the user.
func (r *WorkspaceRepository) CountOwnedByUser(ctx context.Context, userID string) (int, error) {
	_, span := otel.Tracer(workspaceTracerName).Start(ctx, "WorkspaceRepository.CountOwnedByUser")
	defer span.End()

	var count int
	err := r.db.Replica().QueryRow(ctx,
		`SELECT COUNT(*) FROM workspaces WHERE owner_id = $1`, userID,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("counting owned workspaces: %w", err)
	}
	return count, nil
}

// ── Member operations ─────────────────────────────────────────────────────────

// AddMember adds a user to a workspace.
// Returns ErrMemberAlreadyExists on duplicate primary key (workspace_id, user_id).
func (r *WorkspaceRepository) AddMember(ctx context.Context, m *domainworkspace.Member) error {
	ctx, span := otel.Tracer(workspaceTracerName).Start(ctx, "WorkspaceRepository.AddMember",
		trace.WithAttributes(
			attribute.String("workspace.id", m.WorkspaceID),
			attribute.String("user.id", m.UserID),
		),
	)
	defer span.End()

	const query = `
		INSERT INTO workspace_members (workspace_id, user_id, role, invited_by, joined_at)
		VALUES ($1, $2, $3, $4, $5)`

	_, err := r.db.Primary().Exec(ctx, query,
		m.WorkspaceID, m.UserID, string(m.Role),
		nilIfEmpty(m.InvitedBy), m.JoinedAt,
	)
	if err != nil {
		span.RecordError(err)
		return translateWorkspaceError(err)
	}
	return nil
}

// GetMember retrieves a user's membership record.
// This is the hot path — called on every authenticated API request.
// Uses the read replica to distribute load.
func (r *WorkspaceRepository) GetMember(ctx context.Context, workspaceID, userID string) (*domainworkspace.Member, error) {
	ctx, span := otel.Tracer(workspaceTracerName).Start(ctx, "WorkspaceRepository.GetMember",
		trace.WithAttributes(
			attribute.String("workspace.id", workspaceID),
			attribute.String("user.id", userID),
		),
	)
	defer span.End()

	const query = `
		SELECT workspace_id, user_id, role, COALESCE(invited_by, ''), joined_at
		FROM workspace_members
		WHERE workspace_id = $1 AND user_id = $2`

	var m domainworkspace.Member
	var role string
	err := r.db.Replica().QueryRow(ctx, query, workspaceID, userID).Scan(
		&m.WorkspaceID, &m.UserID, &role, &m.InvitedBy, &m.JoinedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, domainworkspace.ErrMemberNotFound
		}
		span.RecordError(err)
		return nil, fmt.Errorf("getting member: %w", err)
	}
	m.Role = domainworkspace.Role(role)
	return &m, nil
}

// ListMembers returns all members of a workspace ordered by joined_at.
func (r *WorkspaceRepository) ListMembers(ctx context.Context, workspaceID string) ([]*domainworkspace.Member, error) {
	_, span := otel.Tracer(workspaceTracerName).Start(ctx, "WorkspaceRepository.ListMembers",
		trace.WithAttributes(
			attribute.String("workspace.id", workspaceID),
		),
	)
	defer span.End()

	const query = `
		SELECT workspace_id, user_id, role, COALESCE(invited_by, ''), joined_at
		FROM workspace_members
		WHERE workspace_id = $1
		ORDER BY joined_at ASC`

	rows, err := r.db.Replica().Query(ctx, query, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("listing members: %w", err)
	}
	defer rows.Close()

	var members []*domainworkspace.Member
	for rows.Next() {
		var m domainworkspace.Member
		var role string
		if err := rows.Scan(&m.WorkspaceID, &m.UserID, &role, &m.InvitedBy, &m.JoinedAt); err != nil {
			return nil, fmt.Errorf("scanning member: %w", err)
		}
		m.Role = domainworkspace.Role(role)
		members = append(members, &m)
	}
	return members, rows.Err()
}

// UpdateRole changes a member's role.
func (r *WorkspaceRepository) UpdateRole(
	ctx context.Context,
	workspaceID, userID string,
	role domainworkspace.Role,
) error {
	ctx, span := otel.Tracer(workspaceTracerName).Start(ctx, "WorkspaceRepository.UpdateRole")
	defer span.End()

	tag, err := r.db.Primary().Exec(ctx,
		`UPDATE workspace_members SET role = $1 WHERE workspace_id = $2 AND user_id = $3`,
		string(role), workspaceID, userID,
	)
	if err != nil {
		span.RecordError(err)
		return fmt.Errorf("updating role: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domainworkspace.ErrMemberNotFound
	}
	return nil
}

// RemoveMember removes a user's membership.
func (r *WorkspaceRepository) RemoveMember(ctx context.Context, workspaceID, userID string) error {
	ctx, span := otel.Tracer(workspaceTracerName).Start(ctx, "WorkspaceRepository.RemoveMember")
	defer span.End()

	tag, err := r.db.Primary().Exec(ctx,
		`DELETE FROM workspace_members WHERE workspace_id = $1 AND user_id = $2`,
		workspaceID, userID,
	)
	if err != nil {
		span.RecordError(err)
		return fmt.Errorf("removing member: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domainworkspace.ErrMemberNotFound
	}
	return nil
}

// ── Scanning helpers ──────────────────────────────────────────────────────────

type workspaceRowScanner interface {
	Scan(dest ...any) error
}

func scanWorkspace(row workspaceRowScanner) (*domainworkspace.Workspace, error) {
	var w domainworkspace.Workspace
	err := row.Scan(
		&w.ID, &w.Name, &w.Slug, &w.PlanTier,
		&w.OwnerID, &w.CreatedAt, &w.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &w, nil
}

// ── Error translation ─────────────────────────────────────────────────────────

// translateWorkspaceError maps pgx errors to workspace domain errors.
// Reuses the same PostgreSQL error code constants from errors.go.
func translateWorkspaceError(err error) error {
	if err == nil {
		return nil
	}
	if err == pgx.ErrNoRows {
		return domainworkspace.ErrNotFound
	}
	// translateError from the existing errors.go handles pg error codes.
	// We map ErrConflict to workspace-specific conflicts here.
	translated := translateError(err, "workspace")
	if translated != nil {
		// Differentiate slug vs name vs member conflicts by constraint name.
		if isConstraintError(err, "workspaces_slug_unique") {
			return domainworkspace.ErrSlugConflict
		}
		if isConstraintError(err, "workspaces_name_unique") {
			return domainworkspace.ErrNameConflict
		}
		if isConstraintError(err, "workspace_members_pkey") {
			return domainworkspace.ErrMemberAlreadyExists
		}
	}
	return translated
}

// isConstraintError returns true if the pgx error is a constraint violation
// on the named constraint.
func isConstraintError(err error, constraintName string) bool {
	if err == nil {
		return false
	}
	return containsString(err.Error(), constraintName)
}

func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr ||
		len(s) > 0 && containsAt(s, substr))
}

func containsAt(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// nilIfEmpty returns nil for an empty string (for nullable DB columns).
func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// Suppress unused import warning for time
var _ = time.Now
