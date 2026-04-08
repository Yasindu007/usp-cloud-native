package apikey

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"

	"github.com/urlshortener/platform/internal/application/apperrors"
	domainapikey "github.com/urlshortener/platform/internal/domain/apikey"
	domainworkspace "github.com/urlshortener/platform/internal/domain/workspace"
)

// ListQuery carries inputs for the ListAPIKeys use case.
type ListQuery struct {
	WorkspaceID      string
	RequestingUserID string
}

// KeySummary is a single API key in the list response.
// RawKey is NEVER included in list responses — only in the create response.
type KeySummary struct {
	ID          string
	Name        string
	KeyPrefix   string
	Scopes      []string
	CreatedAt   string
	ExpiresAt   *time.Time
	LastUsedAt  *time.Time
}

// ListHandler returns all active API keys for a workspace.
type ListHandler struct {
	repo       domainapikey.Repository
	memberRepo domainworkspace.MemberRepository
}

// NewListHandler creates a ListHandler.
func NewListHandler(
	repo domainapikey.Repository,
	memberRepo domainworkspace.MemberRepository,
) *ListHandler {
	return &ListHandler{repo: repo, memberRepo: memberRepo}
}

// Handle executes the ListAPIKeys use case.
// Any workspace member can list keys (all roles can view key metadata).
// RawKey values are never returned.
func (h *ListHandler) Handle(ctx context.Context, q ListQuery) ([]*KeySummary, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "ListAPIKeys.Handle",
		attribute.String("workspace.id", q.WorkspaceID),
	)
	defer span.End()

	// Verify membership (any role can list keys).
	if _, err := h.memberRepo.GetMember(ctx, q.WorkspaceID, q.RequestingUserID); err != nil {
		if domainworkspace.IsNotFound(err) {
			return nil, apperrors.ErrUnauthorized
		}
		return nil, fmt.Errorf("checking membership: %w", err)
	}

	keys, err := h.repo.List(ctx, q.WorkspaceID)
	if err != nil {
		return nil, fmt.Errorf("listing api keys: %w", err)
	}

	summaries := make([]*KeySummary, 0, len(keys))
	for _, k := range keys {
		summaries = append(summaries, &KeySummary{
			ID:         k.ID,
			Name:       k.Name,
			KeyPrefix:  k.KeyPrefix,
			Scopes:     k.Scopes,
			CreatedAt:  k.CreatedAt.Format(time.RFC3339),
			ExpiresAt:  k.ExpiresAt,
			LastUsedAt: k.LastUsedAt,
		})
	}
	return summaries, nil
}