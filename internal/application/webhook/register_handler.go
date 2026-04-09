package webhook

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
	domainwebhook "github.com/urlshortener/platform/internal/domain/webhook"
	"github.com/urlshortener/platform/pkg/webhooksig"
)

const tracerName = "github.com/urlshortener/platform/internal/application/webhook"

type RegisterCommand struct {
	WorkspaceID      string
	RequestingUserID string
	Name             string
	URL              string
	Events           []string
}

type RegisterResult struct {
	ID          string
	WorkspaceID string
	Name        string
	URL         string
	Secret      string
	Events      []string
	Status      string
	CreatedAt   string
}

type RegisterHandler struct {
	repo domainwebhook.Repository
	log  *slog.Logger
}

func NewRegisterHandler(repo domainwebhook.Repository, log *slog.Logger) *RegisterHandler {
	return &RegisterHandler{repo: repo, log: log}
}

func (h *RegisterHandler) Handle(ctx context.Context, cmd RegisterCommand) (*RegisterResult, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "RegisterWebhook.Handle",
		trace.WithAttributes(attribute.String("workspace.id", cmd.WorkspaceID)),
	)
	defer span.End()

	if cmd.Name == "" {
		return nil, apperrors.NewValidationError(domainwebhook.ErrNameRequired.Error(), domainwebhook.ErrNameRequired)
	}
	if cmd.URL == "" {
		return nil, apperrors.NewValidationError(domainwebhook.ErrURLRequired.Error(), domainwebhook.ErrURLRequired)
	}
	if !domainwebhook.ValidateURL(cmd.URL) {
		return nil, apperrors.NewValidationError(domainwebhook.ErrInvalidURL.Error(), domainwebhook.ErrInvalidURL)
	}
	if len(cmd.Events) == 0 {
		return nil, apperrors.NewValidationError(domainwebhook.ErrNoEvents.Error(), domainwebhook.ErrNoEvents)
	}
	for _, event := range cmd.Events {
		if !domainwebhook.IsValidEventType(event) {
			return nil, apperrors.NewValidationError(domainwebhook.ErrInvalidEvents.Error(), domainwebhook.ErrInvalidEvents)
		}
	}

	count, err := h.repo.CountByWorkspace(ctx, cmd.WorkspaceID)
	if err != nil {
		return nil, fmt.Errorf("counting webhooks: %w", err)
	}
	if count >= domainwebhook.MaxWebhooksPerWorkspace {
		return nil, apperrors.NewValidationError(domainwebhook.ErrLimitReached.Error(), domainwebhook.ErrLimitReached)
	}

	secret, err := webhooksig.GenerateSecret()
	if err != nil {
		return nil, fmt.Errorf("generating webhook secret: %w", err)
	}

	now := time.Now().UTC()
	webhook := &domainwebhook.Webhook{
		ID:          ulid.Make().String(),
		WorkspaceID: cmd.WorkspaceID,
		Name:        cmd.Name,
		URL:         cmd.URL,
		Secret:      secret,
		Events:      append([]string(nil), cmd.Events...),
		Status:      domainwebhook.StatusActive,
		CreatedBy:   cmd.RequestingUserID,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := h.repo.Create(ctx, webhook); err != nil {
		return nil, fmt.Errorf("creating webhook: %w", err)
	}

	h.log.Info("webhook registered", slog.String("id", webhook.ID), slog.String("workspace_id", webhook.WorkspaceID))

	return &RegisterResult{
		ID:          webhook.ID,
		WorkspaceID: webhook.WorkspaceID,
		Name:        webhook.Name,
		URL:         webhook.URL,
		Secret:      secret,
		Events:      append([]string(nil), webhook.Events...),
		Status:      string(webhook.Status),
		CreatedAt:   webhook.CreatedAt.Format(time.RFC3339),
	}, nil
}
