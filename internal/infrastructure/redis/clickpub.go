package redis

import (
	"context"
	"encoding/json"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/urlshortener/platform/internal/domain/analytics"
)

const clickTracerName = "github.com/urlshortener/platform/internal/infrastructure/redis/click"

// ClickPublisher publishes real-time click events to Redis Pub/Sub.
type ClickPublisher struct {
	client *Client
}

// NewClickPublisher creates a ClickPublisher.
func NewClickPublisher(client *Client) *ClickPublisher {
	return &ClickPublisher{client: client}
}

// Publish serializes and publishes a click event on its channel.
func (p *ClickPublisher) Publish(ctx context.Context, evt *analytics.ClickEvent) error {
	_, span := otel.Tracer(clickTracerName).Start(ctx, "ClickPublisher.Publish",
		trace.WithAttributes(
			attribute.String("analytics.short_code", evt.ShortCode),
			attribute.String("analytics.workspace_id", evt.WorkspaceID),
			attribute.String("analytics.channel", evt.Channel()),
		),
	)
	defer span.End()

	data, err := json.Marshal(evt)
	if err != nil {
		return fmt.Errorf("clickpub: marshaling event: %w", err)
	}

	if err := p.client.rdb.Publish(ctx, evt.Channel(), data).Err(); err != nil {
		span.RecordError(err)
		return fmt.Errorf("clickpub: publishing to %s: %w", evt.Channel(), err)
	}

	return nil
}
