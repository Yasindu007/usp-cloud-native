package workspace

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/oklog/ulid/v2"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/urlshortener/platform/internal/application/apperrors"
	domainworkspace "github.com/urlshortener/platform/internal/domain/workspace"
)

// AddMemberCommand carries inputs for the AddMember use case.
type AddMemberCommand struct {
	WorkspaceID      string
	InvitedUserID    string
	Role             string
	RequestingUserID string // must have "manage members" permission
}

// AddMemberResult is returned on successful member addition.
type AddMemberResult struct {
	WorkspaceID string
	UserID      string
	Role        string
	JoinedAt    string
}

// AddMemberHandler orchestrates adding a user to a workspace.
type AddMemberHandler struct {
	repo       domainworkspace.Repository
	memberRepo domainworkspace.MemberRepository
	log        *slog.Logger
}

// NewAddMemberHandler creates an AddMemberHandler.
func NewAddMemberHandler(
	repo domainworkspace.Repository,
	memberRepo domainworkspace.MemberRepository,
	log *slog.Logger,
) *AddMemberHandler {
	return &AddMemberHandler{repo: repo, memberRepo: memberRepo, log: log}
}

// Handle executes the AddMember use case.
//
// Authorization:
//
//	The requesting user must be a member with ManageMembers permission.
//	The "owner" role cannot be assigned via this endpoint —
//	ownership is set at workspace creation and transferred via a
//	dedicated TransferOwnership use case (Phase 3).
func (h *AddMemberHandler) Handle(ctx context.Context, cmd AddMemberCommand) (*AddMemberResult, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "AddMember.Handle",
		trace.WithAttributes(
			attribute.String("workspace.id", cmd.WorkspaceID),
			attribute.String("invited_user.id", cmd.InvitedUserID),
		),
	)
	defer span.End()

	// ── Validate role ─────────────────────────────────────────────────────────
	role := domainworkspace.Role(cmd.Role)
	if !role.IsValid() {
		return nil, apperrors.NewValidationError(
			"role must be one of: admin, editor, viewer", nil)
	}
	// Prevent assigning "owner" role via the invite flow.
	if role == domainworkspace.RoleOwner {
		return nil, apperrors.NewValidationError(
			"the owner role cannot be assigned via invitation", nil)
	}

	// ── Authorize requesting user ─────────────────────────────────────────────
	requestingMember, err := h.memberRepo.GetMember(ctx, cmd.WorkspaceID, cmd.RequestingUserID)
	if err != nil {
		if domainworkspace.IsNotFound(err) {
			return nil, apperrors.ErrUnauthorized
		}
		return nil, fmt.Errorf("fetching requesting member: %w", err)
	}

	if !requestingMember.Role.Can(domainworkspace.ActionManageMembers) {
		return nil, apperrors.ErrUnauthorized
	}

	// ── Build member entity ───────────────────────────────────────────────────
	now := time.Now().UTC()
	_ = ulid.Make().String() // generate ID if needed in future

	member := &domainworkspace.Member{
		WorkspaceID: cmd.WorkspaceID,
		UserID:      cmd.InvitedUserID,
		Role:        role,
		InvitedBy:   cmd.RequestingUserID,
		JoinedAt:    now,
	}

	if err := member.Validate(); err != nil {
		return nil, apperrors.NewValidationError(err.Error(), err)
	}

	// ── Persist ───────────────────────────────────────────────────────────────
	if err := h.memberRepo.AddMember(ctx, member); err != nil {
		if domainworkspace.IsConflict(err) {
			return nil, apperrors.NewValidationError("user is already a member of this workspace", err)
		}
		span.RecordError(err)
		return nil, fmt.Errorf("adding member: %w", err)
	}

	h.log.Info("member added to workspace",
		slog.String("workspace_id", cmd.WorkspaceID),
		slog.String("user_id", cmd.InvitedUserID),
		slog.String("role", string(role)),
		slog.String("invited_by", cmd.RequestingUserID),
	)

	return &AddMemberResult{
		WorkspaceID: cmd.WorkspaceID,
		UserID:      cmd.InvitedUserID,
		Role:        string(role),
		JoinedAt:    now.Format(time.RFC3339),
	}, nil
}

// ListMembersQuery carries inputs for the ListMembers use case.
type ListMembersQuery struct {
	WorkspaceID      string
	RequestingUserID string
}

// MemberResult represents one member in a list response.
type MemberResult struct {
	UserID    string
	Role      string
	JoinedAt  string
	InvitedBy string
}

// ListMembersHandler lists workspace members.
type ListMembersHandler struct {
	memberRepo domainworkspace.MemberRepository
}

// NewListMembersHandler creates a ListMembersHandler.
func NewListMembersHandler(memberRepo domainworkspace.MemberRepository) *ListMembersHandler {
	return &ListMembersHandler{memberRepo: memberRepo}
}

// Handle executes the ListMembers use case.
// The requesting user must be a member (any role) to list members.
func (h *ListMembersHandler) Handle(ctx context.Context, q ListMembersQuery) ([]*MemberResult, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "ListMembers.Handle",
		trace.WithAttributes(
			attribute.String("workspace.id", q.WorkspaceID),
		),
	)
	defer span.End()

	// Verify the requesting user is a member.
	requesting, err := h.memberRepo.GetMember(ctx, q.WorkspaceID, q.RequestingUserID)
	if err != nil {
		if domainworkspace.IsNotFound(err) {
			return nil, apperrors.ErrUnauthorized
		}
		return nil, fmt.Errorf("verifying membership: %w", err)
	}

	if !requesting.Role.Can(domainworkspace.ActionViewMembers) {
		return nil, apperrors.ErrUnauthorized
	}

	members, err := h.memberRepo.ListMembers(ctx, q.WorkspaceID)
	if err != nil {
		return nil, fmt.Errorf("listing members: %w", err)
	}

	results := make([]*MemberResult, 0, len(members))
	for _, m := range members {
		results = append(results, &MemberResult{
			UserID:    m.UserID,
			Role:      string(m.Role),
			JoinedAt:  m.JoinedAt.Format(time.RFC3339),
			InvitedBy: m.InvitedBy,
		})
	}
	return results, nil
}
