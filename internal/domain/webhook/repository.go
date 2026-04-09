package webhook

import (
	"context"
	"time"
)

type Repository interface {
	Create(ctx context.Context, w *Webhook) error
	GetByID(ctx context.Context, id, workspaceID string) (*Webhook, error)
	ListByWorkspace(ctx context.Context, workspaceID string, limit int) ([]*Webhook, error)
	CountByWorkspace(ctx context.Context, workspaceID string) (int, error)
	Delete(ctx context.Context, id, workspaceID string) error
	UpdateStatus(ctx context.Context, id string, status Status, failureCount int, lastFailureAt *time.Time) error
	UpdateSuccess(ctx context.Context, id string, at time.Time) error
	FindSubscribed(ctx context.Context, workspaceID string, eventType EventType) ([]*Webhook, error)
}

type DeliveryRepository interface {
	CreateDelivery(ctx context.Context, d *Delivery) error
	ClaimPending(ctx context.Context, limit int) ([]*Delivery, error)
	MarkDelivered(ctx context.Context, id string, httpStatus int, at time.Time) error
	MarkFailed(ctx context.Context, id string, attemptCount int, httpStatus *int, errMsg string, nextAttemptAt *time.Time) error
}
