package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	domainwebhook "github.com/urlshortener/platform/internal/domain/webhook"
)

const webhookTracerName = "github.com/urlshortener/platform/internal/infrastructure/postgres/webhook"
const deliveryClaimTimeout = 5 * time.Minute

type WebhookRepository struct {
	db *Client
}

func NewWebhookRepository(db *Client) *WebhookRepository {
	return &WebhookRepository{db: db}
}

func (r *WebhookRepository) Create(ctx context.Context, w *domainwebhook.Webhook) error {
	_, err := r.db.Primary().Exec(ctx, `
		INSERT INTO webhooks (
			id, workspace_id, name, url, secret, events, status, created_by, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		w.ID, w.WorkspaceID, w.Name, w.URL, w.Secret, w.Events, string(w.Status), w.CreatedBy, w.CreatedAt, w.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert webhook: %w", err)
	}
	return nil
}

func (r *WebhookRepository) GetByID(ctx context.Context, id, workspaceID string) (*domainwebhook.Webhook, error) {
	ctx, span := otel.Tracer(webhookTracerName).Start(ctx, "WebhookRepository.GetByID",
		trace.WithAttributes(attribute.String("webhook.id", id)),
	)
	defer span.End()

	row := r.db.Primary().QueryRow(ctx, `
		SELECT id, workspace_id, name, url, secret, events, status, created_by, created_at, updated_at,
		       last_success_at, last_failure_at, failure_count
		FROM webhooks WHERE id = $1 AND workspace_id = $2`, id, workspaceID)
	wh, err := scanWebhook(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domainwebhook.ErrNotFound
		}
		return nil, fmt.Errorf("get webhook by id: %w", err)
	}
	return wh, nil
}

func (r *WebhookRepository) ListByWorkspace(ctx context.Context, workspaceID string, limit int) ([]*domainwebhook.Webhook, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := r.db.Replica().Query(ctx, `
		SELECT id, workspace_id, name, url, secret, events, status, created_by, created_at, updated_at,
		       last_success_at, last_failure_at, failure_count
		FROM webhooks
		WHERE workspace_id = $1
		ORDER BY created_at DESC
		LIMIT $2`, workspaceID, limit)
	if err != nil {
		return nil, fmt.Errorf("list webhooks: %w", err)
	}
	defer rows.Close()

	var out []*domainwebhook.Webhook
	for rows.Next() {
		wh, err := scanWebhook(rows)
		if err != nil {
			return nil, fmt.Errorf("scan webhook: %w", err)
		}
		out = append(out, wh)
	}
	return out, rows.Err()
}

func (r *WebhookRepository) CountByWorkspace(ctx context.Context, workspaceID string) (int, error) {
	var count int
	err := r.db.Replica().QueryRow(ctx, `
		SELECT COUNT(*) FROM webhooks WHERE workspace_id = $1 AND status != 'disabled'`, workspaceID,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count webhooks: %w", err)
	}
	return count, nil
}

func (r *WebhookRepository) Delete(ctx context.Context, id, workspaceID string) error {
	tag, err := r.db.Primary().Exec(ctx, `DELETE FROM webhooks WHERE id = $1 AND workspace_id = $2`, id, workspaceID)
	if err != nil {
		return fmt.Errorf("delete webhook: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domainwebhook.ErrNotFound
	}
	return nil
}

func (r *WebhookRepository) UpdateStatus(ctx context.Context, id string, status domainwebhook.Status, failureCount int, lastFailureAt *time.Time) error {
	_, err := r.db.Primary().Exec(ctx, `
		UPDATE webhooks
		SET status = $2, failure_count = $3, last_failure_at = $4
		WHERE id = $1`, id, string(status), failureCount, lastFailureAt)
	if err != nil {
		return fmt.Errorf("update webhook status: %w", err)
	}
	return nil
}

func (r *WebhookRepository) UpdateSuccess(ctx context.Context, id string, at time.Time) error {
	_, err := r.db.Primary().Exec(ctx, `
		UPDATE webhooks
		SET status = 'active', failure_count = 0, last_success_at = $2, last_failure_at = NULL
		WHERE id = $1`, id, at)
	if err != nil {
		return fmt.Errorf("update webhook success: %w", err)
	}
	return nil
}

func (r *WebhookRepository) FindSubscribed(ctx context.Context, workspaceID string, eventType domainwebhook.EventType) ([]*domainwebhook.Webhook, error) {
	rows, err := r.db.Replica().Query(ctx, `
		SELECT id, workspace_id, name, url, secret, events, status, created_by, created_at, updated_at,
		       last_success_at, last_failure_at, failure_count
		FROM webhooks
		WHERE workspace_id = $1
		  AND status = 'active'
		  AND $2 = ANY(events)`, workspaceID, string(eventType))
	if err != nil {
		return nil, fmt.Errorf("find subscribed webhooks: %w", err)
	}
	defer rows.Close()

	var out []*domainwebhook.Webhook
	for rows.Next() {
		wh, err := scanWebhook(rows)
		if err != nil {
			return nil, fmt.Errorf("scan subscribed webhook: %w", err)
		}
		out = append(out, wh)
	}
	return out, rows.Err()
}

func (r *WebhookRepository) CreateDelivery(ctx context.Context, d *domainwebhook.Delivery) error {
	_, err := r.db.Primary().Exec(ctx, `
		INSERT INTO webhook_deliveries (
			id, webhook_id, workspace_id, event_type, event_id, payload, status,
			attempt_count, next_attempt_at, created_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		d.ID, d.WebhookID, d.WorkspaceID, string(d.EventType), d.EventID, d.Payload, string(d.Status), d.AttemptCount, d.NextAttemptAt, d.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("create webhook delivery: %w", err)
	}
	return nil
}

func (r *WebhookRepository) ClaimPending(ctx context.Context, limit int) ([]*domainwebhook.Delivery, error) {
	if limit <= 0 {
		limit = 50
	}
	staleBefore := time.Now().UTC().Add(-deliveryClaimTimeout)
	rows, err := r.db.Primary().Query(ctx, `
		WITH next_jobs AS (
			SELECT id
			FROM webhook_deliveries
			WHERE (
				(status = 'pending' AND next_attempt_at <= NOW())
				OR
				(status = 'delivering' AND last_attempt_at IS NOT NULL AND last_attempt_at <= $2)
			)
			ORDER BY COALESCE(next_attempt_at, last_attempt_at) ASC
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		UPDATE webhook_deliveries d
		SET status = 'delivering',
		    attempt_count = d.attempt_count + 1,
		    last_attempt_at = NOW()
		FROM next_jobs
		WHERE d.id = next_jobs.id
		RETURNING d.id, d.webhook_id, d.workspace_id, d.event_type, d.event_id, d.payload, d.status,
		          d.attempt_count, d.next_attempt_at, d.last_attempt_at, d.last_http_status,
		          COALESCE(d.last_error, ''), d.delivered_at, d.created_at`, limit, staleBefore)
	if err != nil {
		return nil, fmt.Errorf("claim pending deliveries: %w", err)
	}
	defer rows.Close()

	var out []*domainwebhook.Delivery
	for rows.Next() {
		d, err := scanDelivery(rows)
		if err != nil {
			return nil, fmt.Errorf("scan claimed delivery: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (r *WebhookRepository) MarkDelivered(ctx context.Context, id string, httpStatus int, at time.Time) error {
	_, err := r.db.Primary().Exec(ctx, `
		UPDATE webhook_deliveries
		SET status = 'delivered', delivered_at = $2, last_http_status = $3
		WHERE id = $1`, id, at, httpStatus)
	if err != nil {
		return fmt.Errorf("mark delivery delivered: %w", err)
	}
	return nil
}

func (r *WebhookRepository) MarkFailed(ctx context.Context, id string, attemptCount int, httpStatus *int, errMsg string, nextAttemptAt *time.Time) error {
	status := "abandoned"
	if nextAttemptAt != nil {
		status = "pending"
	}
	_, err := r.db.Primary().Exec(ctx, `
		UPDATE webhook_deliveries
		SET status = $2,
		    last_http_status = $3,
		    last_error = $4,
		    next_attempt_at = COALESCE($5, next_attempt_at)
		WHERE id = $1`, id, status, httpStatus, errMsg, nextAttemptAt)
	if err != nil {
		return fmt.Errorf("mark delivery failed: %w", err)
	}
	return nil
}

type webhookScanner interface {
	Scan(dest ...any) error
}

func scanWebhook(row webhookScanner) (*domainwebhook.Webhook, error) {
	var wh domainwebhook.Webhook
	var status string
	if err := row.Scan(
		&wh.ID, &wh.WorkspaceID, &wh.Name, &wh.URL, &wh.Secret, &wh.Events, &status, &wh.CreatedBy,
		&wh.CreatedAt, &wh.UpdatedAt, &wh.LastSuccessAt, &wh.LastFailureAt, &wh.FailureCount,
	); err != nil {
		return nil, err
	}
	wh.Status = domainwebhook.Status(status)
	return &wh, nil
}

func scanDelivery(row webhookScanner) (*domainwebhook.Delivery, error) {
	var d domainwebhook.Delivery
	var status, eventType string
	if err := row.Scan(
		&d.ID, &d.WebhookID, &d.WorkspaceID, &eventType, &d.EventID, &d.Payload, &status,
		&d.AttemptCount, &d.NextAttemptAt, &d.LastAttemptAt, &d.LastHTTPStatus,
		&d.LastError, &d.DeliveredAt, &d.CreatedAt,
	); err != nil {
		return nil, err
	}
	d.Status = domainwebhook.DeliveryStatus(status)
	d.EventType = domainwebhook.EventType(eventType)
	return &d, nil
}
