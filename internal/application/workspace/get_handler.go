package workspace

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	domainworkspace "github.com/urlshortener/platform/internal/domain/workspace"
)

// GetQuery carries inputs for the GetWorkspace use case.
type GetQuery struct {
	// WorkspaceID is the ULID to look up.
	WorkspaceID string

	// RequestingUserID is the user making the request.
	// The use case verifies the user is a member before returning data.
	// This prevents workspace enumeration by non-members.
	RequestingUserID string
}

// GetResult is returned on successful workspace retrieval.
type GetResult struct {
	ID        string
	Name      string
	Slug      string
	PlanTier  string
	OwnerID   string
	CreatedAt string
	// UserRole is the requesting user's role in this workspace.
	// Included so the client can render role-appropriate UI without
	// a separate membership API call.
	UserRole string
}

// GetHandler orchestrates the GetWorkspace use case.
type GetHandler struct {
	repo       domainworkspace.Repository
	memberRepo domainworkspace.MemberRepository
}

// NewGetHandler creates a GetHandler.
func NewGetHandler(repo domainworkspace.Repository, memberRepo domainworkspace.MemberRepository) *GetHandler {
	return &GetHandler{repo: repo, memberRepo: memberRepo}
}

// Handle executes the GetWorkspace use case.
// Returns ErrNotMember if the requesting user is not a member —
// this prevents workspace ID enumeration by unauthorized users.
func (h *GetHandler) Handle(ctx context.Context, q GetQuery) (*GetResult, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "GetWorkspace.Handle",
		trace.WithAttributes(
			attribute.String("workspace.id", q.WorkspaceID),
			attribute.String("user.id", q.RequestingUserID),
		),
	)
	defer span.End()

	// Verify membership before fetching workspace data.
	// This is the correct order: auth check THEN data fetch.
	// Doing it in reverse leaks workspace existence to non-members.
	member, err := h.memberRepo.GetMember(ctx, q.WorkspaceID, q.RequestingUserID)
	if err != nil {
		if domainworkspace.IsNotFound(err) {
			return nil, domainworkspace.ErrNotMember
		}
		return nil, fmt.Errorf("checking membership: %w", err)
	}

	w, err := h.repo.GetByID(ctx, q.WorkspaceID)
	if err != nil {
		if domainworkspace.IsNotFound(err) {
			return nil, domainworkspace.ErrNotFound
		}
		return nil, fmt.Errorf("fetching workspace: %w", err)
	}

	return &GetResult{
		ID:        w.ID,
		Name:      w.Name,
		Slug:      w.Slug,
		PlanTier:  w.PlanTier,
		OwnerID:   w.OwnerID,
		CreatedAt: w.CreatedAt.Format(time.RFC3339),
		UserRole:  string(member.Role),
	}, nil
}

// ListQuery carries inputs for the ListWorkspaces use case.
type ListQuery struct {
	UserID string
}

// ListResult is a single workspace summary in a list response.
type ListResult struct {
	ID       string
	Name     string
	Slug     string
	PlanTier string
	UserRole string
}

// ListHandler returns all workspaces the requesting user belongs to.
type ListHandler struct {
	repo       domainworkspace.Repository
	memberRepo domainworkspace.MemberRepository
}

// NewListHandler creates a ListHandler.
func NewListHandler(repo domainworkspace.Repository, memberRepo domainworkspace.MemberRepository) *ListHandler {
	return &ListHandler{repo: repo, memberRepo: memberRepo}
}

// Handle executes the ListWorkspaces use case.
func (h *ListHandler) Handle(ctx context.Context, q ListQuery) ([]*ListResult, error) {
	_, span := otel.Tracer(tracerName).Start(ctx, "ListWorkspaces.Handle",
		trace.WithAttributes(
			attribute.String("user.id", q.UserID),
		),
	)
	defer span.End()

	workspaces, err := h.repo.ListForUser(ctx, q.UserID)
	if err != nil {
		return nil, fmt.Errorf("listing workspaces: %w", err)
	}

	results := make([]*ListResult, 0, len(workspaces))
	for _, w := range workspaces {
		member, err := h.memberRepo.GetMember(ctx, w.ID, q.UserID)
		if err != nil {
			continue // defensive: skip if membership fetch fails
		}
		results = append(results, &ListResult{
			ID:       w.ID,
			Name:     w.Name,
			Slug:     w.Slug,
			PlanTier: w.PlanTier,
			UserRole: string(member.Role),
		})
	}

	return results, nil
}
