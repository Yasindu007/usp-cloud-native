package webhook

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	domainwebhook "github.com/urlshortener/platform/internal/domain/webhook"
)

type workerTestRepo struct {
	getByIDHook    *domainwebhook.Webhook
	getByIDErr     error
	updateStatusID string
}

func (r *workerTestRepo) Create(context.Context, *domainwebhook.Webhook) error { return nil }
func (r *workerTestRepo) GetByID(context.Context, string, string) (*domainwebhook.Webhook, error) {
	if r.getByIDErr != nil {
		return nil, r.getByIDErr
	}
	return r.getByIDHook, nil
}
func (r *workerTestRepo) ListByWorkspace(context.Context, string, int) ([]*domainwebhook.Webhook, error) {
	return nil, nil
}
func (r *workerTestRepo) CountByWorkspace(context.Context, string) (int, error) { return 0, nil }
func (r *workerTestRepo) Delete(context.Context, string, string) error          { return nil }
func (r *workerTestRepo) UpdateStatus(_ context.Context, id string, _ domainwebhook.Status, _ int, _ *time.Time) error {
	r.updateStatusID = id
	return nil
}
func (r *workerTestRepo) UpdateSuccess(context.Context, string, time.Time) error { return nil }
func (r *workerTestRepo) FindSubscribed(context.Context, string, domainwebhook.EventType) ([]*domainwebhook.Webhook, error) {
	return nil, nil
}

type workerTestDeliveries struct {
	lastID          string
	lastAttempt     int
	lastHTTPStatus  *int
	lastError       string
	lastNextAttempt *time.Time
}

func (d *workerTestDeliveries) CreateDelivery(context.Context, *domainwebhook.Delivery) error {
	return nil
}
func (d *workerTestDeliveries) ClaimPending(context.Context, int) ([]*domainwebhook.Delivery, error) {
	return nil, nil
}
func (d *workerTestDeliveries) MarkDelivered(context.Context, string, int, time.Time) error {
	return nil
}
func (d *workerTestDeliveries) MarkFailed(_ context.Context, id string, attemptCount int, httpStatus *int, errMsg string, nextAttemptAt *time.Time) error {
	d.lastID = id
	d.lastAttempt = attemptCount
	d.lastHTTPStatus = httpStatus
	d.lastError = errMsg
	d.lastNextAttempt = nextAttemptAt
	return nil
}

func TestWorkerProcessDelivery_RequeuesOnWebhookLookupError(t *testing.T) {
	repo := &workerTestRepo{getByIDErr: errors.New("replica unavailable")}
	deliveries := &workerTestDeliveries{}
	worker := NewWorker(repo, deliveries, WorkerConfig{}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	delivery := &domainwebhook.Delivery{
		ID:           "dlv_123",
		WebhookID:    "wh_123",
		WorkspaceID:  "ws_123",
		EventType:    domainwebhook.EventURLCreated,
		Payload:      []byte(`{"id":"dlv_123"}`),
		AttemptCount: 1,
	}

	worker.processDelivery(context.Background(), delivery)

	if deliveries.lastID != delivery.ID {
		t.Fatalf("expected delivery %q to be marked failed, got %q", delivery.ID, deliveries.lastID)
	}
	if deliveries.lastNextAttempt == nil {
		t.Fatal("expected retry to be scheduled after transient webhook lookup failure")
	}
	if repo.updateStatusID != "" {
		t.Fatalf("did not expect webhook status update without a loaded webhook, got %q", repo.updateStatusID)
	}
}
