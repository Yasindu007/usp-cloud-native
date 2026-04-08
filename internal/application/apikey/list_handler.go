package apikey

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/urlshortener/platform/internal/application/apperrors"
	domainapikey "github.com/urlshortener/platform/internal/domain/apikey"
	domainworkspace "github.com/urlshortener/platform/internal/domain/workspace"
)

type ListQuery struct {
	WorkspaceID      string
	RequestingUserID string
}

type KeySummary struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	KeyPrefix  string     `json:"key_prefix"`
	Scopes     []string   `json:"scopes"`
	CreatedAt  string     `json:"created_at"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

type ListHandler struct {
	repo       domainapikey.Repository
	memberRepo domainworkspace.MemberRepository
}

func NewListHandler(repo domainapikey.Repository, memberRepo domainworkspace.MemberRepository) *ListHandler {
	return &ListHandler{repo: repo, memberRepo: memberRepo}
}

func (h *ListHandler) Handle(ctx context.Context, q ListQuery) ([]*KeySummary, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "ListAPIKeys.Handle",
		trace.WithAttributes(
			attribute.String("workspace.id", q.WorkspaceID),
			attribute.String("user.id", q.RequestingUserID),
		),
	)
	defer span.End()

	if _, err := h.memberRepo.GetMember(ctx, q.WorkspaceID, q.RequestingUserID); err != nil {
		if domainworkspace.IsNotFound(err) {
			return nil, apperrors.ErrUnauthorized
		}
		return nil, fmt.Errorf("checking membership: %w", err)
	}

	keys, err := h.repo.List(ctx, q.WorkspaceID)
	if err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("listing api keys: %w", err)
	}

	results := make([]*KeySummary, 0, len(keys))
	for _, k := range keys {
		results = append(results, &KeySummary{
			ID:         k.ID,
			Name:       k.Name,
			KeyPrefix:  k.KeyPrefix,
			Scopes:     k.Scopes,
			CreatedAt:  k.CreatedAt.Format(time.RFC3339),
			ExpiresAt:  k.ExpiresAt,
			LastUsedAt: k.LastUsedAt,
		})
	}
	return results, nil
}
