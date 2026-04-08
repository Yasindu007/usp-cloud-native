package apikey

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
	domainapikey "github.com/urlshortener/platform/internal/domain/apikey"
	"github.com/urlshortener/platform/pkg/keyutil"
)

const tracerName = "github.com/urlshortener/platform/internal/application/apikey"

type CreateCommand struct {
	WorkspaceID string
	Name        string
	Scopes      []string
	ExpiresAt   *time.Time
	CreatedBy   string
}

type CreateResult struct {
	ID          string
	Name        string
	KeyPrefix   string
	RawKey      string
	Scopes      []string
	WorkspaceID string
	CreatedAt   string
	ExpiresAt   *time.Time
}

type CreateHandler struct {
	repo domainapikey.Repository
	log  *slog.Logger
}

func NewCreateHandler(repo domainapikey.Repository, log *slog.Logger) *CreateHandler {
	return &CreateHandler{repo: repo, log: log}
}

func (h *CreateHandler) Handle(ctx context.Context, cmd CreateCommand) (*CreateResult, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "CreateAPIKey.Handle",
		trace.WithAttributes(
			attribute.String("workspace.id", cmd.WorkspaceID),
			attribute.String("apikey.name", cmd.Name),
		),
	)
	defer span.End()

	if cmd.Name == "" {
		return nil, apperrors.NewValidationError("name is required", nil)
	}
	if len(cmd.Name) > 100 {
		return nil, apperrors.NewValidationError("name must be 100 characters or fewer", nil)
	}
	if msg := domainapikey.ValidateScopes(cmd.Scopes); msg != "" {
		return nil, apperrors.NewValidationError(msg, nil)
	}
	if cmd.WorkspaceID == "" {
		return nil, apperrors.NewValidationError("workspace_id is required", nil)
	}
	if cmd.CreatedBy == "" {
		return nil, apperrors.NewValidationError("created_by is required", nil)
	}

	rawKey, err := keyutil.GenerateRaw(cmd.WorkspaceID)
	if err != nil {
		return nil, fmt.Errorf("generating api key: %w", err)
	}

	keyHash, err := keyutil.Hash(rawKey)
	if err != nil {
		return nil, fmt.Errorf("hashing api key: %w", err)
	}

	now := time.Now().UTC()
	ak := &domainapikey.APIKey{
		ID:          ulid.Make().String(),
		WorkspaceID: cmd.WorkspaceID,
		Name:        cmd.Name,
		KeyHash:     keyHash,
		KeyPrefix:   domainapikey.ExtractPrefix(rawKey),
		Scopes:      cmd.Scopes,
		CreatedBy:   cmd.CreatedBy,
		CreatedAt:   now,
		ExpiresAt:   cmd.ExpiresAt,
	}

	if err := h.repo.Create(ctx, ak); err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("creating api key: %w", err)
	}

	h.log.Info("api key created",
		slog.String("id", ak.ID),
		slog.String("name", ak.Name),
		slog.String("workspace_id", ak.WorkspaceID),
		slog.String("key_prefix", ak.KeyPrefix),
		slog.String("created_by", ak.CreatedBy),
	)

	return &CreateResult{
		ID:          ak.ID,
		Name:        ak.Name,
		KeyPrefix:   ak.KeyPrefix,
		RawKey:      rawKey,
		Scopes:      ak.Scopes,
		WorkspaceID: ak.WorkspaceID,
		CreatedAt:   now.Format(time.RFC3339),
		ExpiresAt:   ak.ExpiresAt,
	}, nil
}
