package webhook

import (
	"context"
	"errors"
	"fmt"

	"github.com/urlshortener/platform/internal/application/apperrors"
	domainwebhook "github.com/urlshortener/platform/internal/domain/webhook"
)

type DeleteCommand struct {
	WebhookID        string
	WorkspaceID      string
	RequestingUserID string
}

type DeleteHandler struct {
	repo domainwebhook.Repository
}

func NewDeleteHandler(repo domainwebhook.Repository) *DeleteHandler {
	return &DeleteHandler{repo: repo}
}

func (h *DeleteHandler) Handle(ctx context.Context, cmd DeleteCommand) error {
	if err := h.repo.Delete(ctx, cmd.WebhookID, cmd.WorkspaceID); err != nil {
		if errors.Is(err, domainwebhook.ErrNotFound) {
			return apperrors.ErrNotFound
		}
		return fmt.Errorf("deleting webhook: %w", err)
	}
	return nil
}
