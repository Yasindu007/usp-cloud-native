package webhook

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	domainwebhook "github.com/urlshortener/platform/internal/domain/webhook"
	"github.com/urlshortener/platform/pkg/webhooksig"
)

type WorkerConfig struct {
	BatchSize    int
	PollInterval time.Duration
	HTTPTimeout  time.Duration
}

type Worker struct {
	repo   domainwebhook.Repository
	delivs domainwebhook.DeliveryRepository
	cfg    WorkerConfig
	client *http.Client
	log    *slog.Logger
}

func NewWorker(repo domainwebhook.Repository, deliveries domainwebhook.DeliveryRepository, cfg WorkerConfig, log *slog.Logger) *Worker {
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 50
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 5 * time.Second
	}
	if cfg.HTTPTimeout <= 0 {
		cfg.HTTPTimeout = 30 * time.Second
	}
	return &Worker{
		repo:   repo,
		delivs: deliveries,
		cfg:    cfg,
		client: &http.Client{Timeout: cfg.HTTPTimeout},
		log:    log,
	}
}

func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.cfg.PollInterval)
	defer ticker.Stop()

	for {
		if err := w.processBatch(ctx); err != nil && ctx.Err() == nil {
			w.log.Error("webhook worker iteration failed", slog.String("error", err.Error()))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (w *Worker) processBatch(ctx context.Context) error {
	deliveries, err := w.delivs.ClaimPending(ctx, w.cfg.BatchSize)
	if err != nil {
		return err
	}
	for _, delivery := range deliveries {
		w.processDelivery(ctx, delivery)
	}
	return nil
}

func (w *Worker) processDelivery(ctx context.Context, delivery *domainwebhook.Delivery) {
	hook, err := w.repo.GetByID(ctx, delivery.WebhookID, delivery.WorkspaceID)
	if err != nil {
		if errors.Is(err, domainwebhook.ErrNotFound) {
			_ = w.delivs.MarkFailed(ctx, delivery.ID, delivery.AttemptCount, nil, "webhook no longer exists", nil)
			return
		}
		w.log.Error("load webhook for delivery failed", slog.String("delivery_id", delivery.ID), slog.String("error", err.Error()))
		w.recordFailure(ctx, delivery, nil, nil, "load webhook failed: "+err.Error())
		return
	}
	if !hook.IsDeliverable() {
		_ = w.delivs.MarkFailed(ctx, delivery.ID, delivery.AttemptCount, nil, "webhook is not deliverable", nil)
		return
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, hook.URL, bytes.NewReader(delivery.Payload))
	if err != nil {
		w.recordFailure(ctx, delivery, hook, nil, err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "urlshortener-webhooks/1.0")
	req.Header.Set("X-Webhook-ID", hook.ID)
	req.Header.Set("X-Webhook-Delivery", delivery.ID)
	req.Header.Set("X-Webhook-Event", string(delivery.EventType))
	req.Header.Set("X-Webhook-Signature", webhooksig.Sign(hook.Secret, delivery.Payload))

	resp, err := w.client.Do(req)
	if err != nil {
		w.recordFailure(ctx, delivery, hook, nil, err.Error())
		return
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		now := time.Now().UTC()
		_ = w.delivs.MarkDelivered(ctx, delivery.ID, resp.StatusCode, now)
		_ = w.repo.UpdateSuccess(ctx, hook.ID, now)
		return
	}

	w.recordFailure(ctx, delivery, hook, &resp.StatusCode, resp.Status)
}

func (w *Worker) recordFailure(
	ctx context.Context,
	delivery *domainwebhook.Delivery,
	hook *domainwebhook.Webhook,
	httpStatus *int,
	errMsg string,
) {
	now := time.Now().UTC()
	if delay, ok := domainwebhook.NextRetryDelay(delivery.AttemptCount); ok {
		next := now.Add(delay)
		_ = w.delivs.MarkFailed(ctx, delivery.ID, delivery.AttemptCount, httpStatus, errMsg, &next)
		return
	}
	_ = w.delivs.MarkFailed(ctx, delivery.ID, delivery.AttemptCount, httpStatus, errMsg, nil)
	if hook != nil {
		_ = w.repo.UpdateStatus(ctx, hook.ID, domainwebhook.StatusFailing, hook.FailureCount+1, &now)
	}
}
