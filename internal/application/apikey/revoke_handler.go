package apikey

import (
	"context"
	"fmt"
	"log/slog"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/urlshortener/platform/internal/application/apperrors"
	domainapikey "github.com/urlshortener/platform/internal/domain/apikey"
	domainworkspace "github.com/urlshortener/platform/internal/domain/workspace"
)

type RevokeCommand struct {
	KeyID            string
	WorkspaceID      string
	RequestingUserID string
}

type RevokeHandler struct {
	repo       domainapikey.Repository
	memberRepo domainworkspace.MemberRepository
	log        *slog.Logger
}

func NewRevokeHandler(
	repo domainapikey.Repository,
	memberRepo domainworkspace.MemberRepository,
	log *slog.Logger,
) *RevokeHandler {
	return &RevokeHandler{repo: repo, memberRepo: memberRepo, log: log}
}

func (h *RevokeHandler) Handle(ctx context.Context, cmd RevokeCommand) error {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "RevokeAPIKey.Handle",
		trace.WithAttributes(
			attribute.String("apikey.id", cmd.KeyID),
			attribute.String("workspace.id", cmd.WorkspaceID),
		),
	)
	defer span.End()

	member, err := h.memberRepo.GetMember(ctx, cmd.WorkspaceID, cmd.RequestingUserID)
	if err != nil {
		if domainworkspace.IsNotFound(err) {
			return apperrors.ErrUnauthorized
		}
		return fmt.Errorf("checking membership: %w", err)
	}
	if !member.Role.Can(domainworkspace.ActionManageMembers) {
		return apperrors.ErrUnauthorized
	}

	if err := h.repo.Revoke(ctx, cmd.KeyID, cmd.WorkspaceID); err != nil {
		if err == domainapikey.ErrNotFound {
			return apperrors.ErrNotFound
		}
		if err == domainapikey.ErrAlreadyRevoked {
			return apperrors.NewValidationError("this API key has already been revoked", err)
		}
		span.RecordError(err)
		return fmt.Errorf("revoking api key: %w", err)
	}

	h.log.Info("api key revoked",
		slog.String("key_id", cmd.KeyID),
		slog.String("workspace_id", cmd.WorkspaceID),
		slog.String("revoked_by", cmd.RequestingUserID),
	)

	return nil
}
