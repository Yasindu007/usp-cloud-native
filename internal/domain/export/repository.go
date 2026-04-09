package export

import (
	"context"
	"time"
)

type Repository interface {
	Create(ctx context.Context, e *Export) error
	GetByID(ctx context.Context, id, workspaceID string) (*Export, error)
	ListByWorkspace(ctx context.Context, workspaceID string, limit int) ([]*Export, error)
	ClaimPending(ctx context.Context) (*Export, error)
	MarkCompleted(ctx context.Context, id string, filePath string, rowCount, fileSizeBytes int64, token string, expiresAt time.Time) error
	MarkFailed(ctx context.Context, id, errorMessage string) error
}

type EventReader interface {
	ReadEvents(ctx context.Context, q EventQuery) (<-chan *RedirectEventRow, <-chan error)
}

type EventQuery struct {
	WorkspaceID string
	DateFrom    time.Time
	DateTo      time.Time
	IncludeBots bool
	BatchSize   int
}
