package webhook

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/oklog/ulid/v2"

	domainwebhook "github.com/urlshortener/platform/internal/domain/webhook"
)

type Dispatcher struct {
	repo       domainwebhook.Repository
	deliveries domainwebhook.DeliveryRepository
	log        *slog.Logger
}

func NewDispatcher(repo domainwebhook.Repository, deliveries domainwebhook.DeliveryRepository, log *slog.Logger) *Dispatcher {
	return &Dispatcher{repo: repo, deliveries: deliveries, log: log}
}

func (d *Dispatcher) Dispatch(ctx context.Context, event domainwebhook.Event) error {
	hooks, err := d.repo.FindSubscribed(ctx, event.WorkspaceID, event.Type)
	if err != nil {
		return fmt.Errorf("finding subscribed webhooks: %w", err)
	}
	for _, hook := range hooks {
		deliveryID := ulid.Make().String()
		payload, err := json.Marshal(domainwebhook.EventPayload{
			ID:          deliveryID,
			EventType:   event.Type,
			EventID:     event.EventID,
			WorkspaceID: event.WorkspaceID,
			OccurredAt:  event.OccurredAt.UTC(),
			Data:        event.Data,
		})
		if err != nil {
			return fmt.Errorf("marshal webhook payload: %w", err)
		}
		if err := d.deliveries.CreateDelivery(ctx, &domainwebhook.Delivery{
			ID:            deliveryID,
			WebhookID:     hook.ID,
			WorkspaceID:   hook.WorkspaceID,
			EventType:     event.Type,
			EventID:       event.EventID,
			Payload:       payload,
			Status:        domainwebhook.DeliveryPending,
			AttemptCount:  0,
			NextAttemptAt: time.Now().UTC(),
			CreatedAt:     time.Now().UTC(),
		}); err != nil {
			return fmt.Errorf("create delivery for webhook %s: %w", hook.ID, err)
		}
	}
	if len(hooks) > 0 {
		d.log.Debug("webhook deliveries queued",
			slog.String("workspace_id", event.WorkspaceID),
			slog.String("event_type", string(event.Type)),
			slog.Int("count", len(hooks)),
		)
	}
	return nil
}
