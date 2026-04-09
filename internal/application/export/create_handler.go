package export

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
	domainexport "github.com/urlshortener/platform/internal/domain/export"
)

const tracerName = "github.com/urlshortener/platform/internal/application/export"

type CreateCommand struct {
	WorkspaceID string
	RequestedBy string
	Format      string
	DateFrom    time.Time
	DateTo      time.Time
	IncludeBots bool
}

type CreateResult struct {
	ID          string
	WorkspaceID string
	Format      string
	Status      string
	DateFrom    time.Time
	DateTo      time.Time
	IncludeBots bool
	CreatedAt   time.Time
}

type CreateHandler struct {
	repo          domainexport.Repository
	log           *slog.Logger
	maxWindowDays int
}

func NewCreateHandler(repo domainexport.Repository, log *slog.Logger, maxWindowDays int) *CreateHandler {
	if maxWindowDays <= 0 {
		maxWindowDays = domainexport.MaxWindowDays
	}
	return &CreateHandler{repo: repo, log: log, maxWindowDays: maxWindowDays}
}

func (h *CreateHandler) Handle(ctx context.Context, cmd CreateCommand) (*CreateResult, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "CreateExport.Handle",
		trace.WithAttributes(
			attribute.String("workspace.id", cmd.WorkspaceID),
			attribute.String("export.format", cmd.Format),
		),
	)
	defer span.End()

	if cmd.DateTo.Before(cmd.DateFrom) || cmd.DateTo.Equal(cmd.DateFrom) {
		return nil, apperrors.NewValidationError(domainexport.ErrInvalidDateRange.Error(), domainexport.ErrInvalidDateRange)
	}
	if cmd.DateTo.Sub(cmd.DateFrom) > time.Duration(h.maxWindowDays)*24*time.Hour {
		return nil, apperrors.NewValidationError(domainexport.ErrWindowTooLarge.Error(), domainexport.ErrWindowTooLarge)
	}

	format := domainexport.Format(cmd.Format)
	switch format {
	case domainexport.FormatCSV, domainexport.FormatJSONLines:
	default:
		return nil, apperrors.NewValidationError(domainexport.ErrInvalidFormat.Error(), domainexport.ErrInvalidFormat)
	}

	e := &domainexport.Export{
		ID:          ulid.Make().String(),
		WorkspaceID: cmd.WorkspaceID,
		RequestedBy: cmd.RequestedBy,
		Format:      format,
		Status:      domainexport.StatusPending,
		DateFrom:    cmd.DateFrom.UTC(),
		DateTo:      cmd.DateTo.UTC(),
		IncludeBots: cmd.IncludeBots,
		CreatedAt:   time.Now().UTC(),
	}
	if err := h.repo.Create(ctx, e); err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("creating export job: %w", err)
	}

	h.log.Info("export job created", slog.String("id", e.ID), slog.String("workspace_id", e.WorkspaceID), slog.String("format", string(e.Format)))

	return &CreateResult{
		ID:          e.ID,
		WorkspaceID: e.WorkspaceID,
		Format:      string(e.Format),
		Status:      string(e.Status),
		DateFrom:    e.DateFrom,
		DateTo:      e.DateTo,
		IncludeBots: e.IncludeBots,
		CreatedAt:   e.CreatedAt,
	}, nil
}
