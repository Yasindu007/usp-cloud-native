package redis

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	goredis "github.com/redis/go-redis/v9"

	"github.com/urlshortener/platform/internal/domain/analytics"
)

// ClickSubscription wraps a Redis Pub/Sub subscription.
type ClickSubscription struct {
	ps      *goredis.PubSub
	channel <-chan *goredis.Message
	once    sync.Once
	events  chan *analytics.ClickEvent
}

// ClickEventStream is the consumer-facing stream interface for click events.
type ClickEventStream interface {
	Events() <-chan *analytics.ClickEvent
	Close() error
}

// Events returns decoded click events from the underlying Pub/Sub channel.
func (s *ClickSubscription) Events() <-chan *analytics.ClickEvent {
	s.once.Do(func() {
		s.events = make(chan *analytics.ClickEvent, 16)
		go func() {
			defer close(s.events)
			for msg := range s.channel {
				var evt analytics.ClickEvent
				if err := json.Unmarshal([]byte(msg.Payload), &evt); err != nil {
					continue
				}
				s.events <- &evt
			}
		}()
	})
	return s.events
}

// Close closes the subscription.
func (s *ClickSubscription) Close() error {
	return s.ps.Close()
}

// ClickSubscriber creates Pub/Sub subscriptions for real-time clicks.
type ClickSubscriber struct {
	client *Client
}

// NewClickSubscriber creates a ClickSubscriber.
func NewClickSubscriber(client *Client) *ClickSubscriber {
	return &ClickSubscriber{client: client}
}

// SubscribeToURL subscribes to one URL's click channel.
func (s *ClickSubscriber) SubscribeToURL(
	ctx context.Context,
	workspaceID, shortCode string,
) (ClickEventStream, error) {
	channel := analytics.ChannelName(workspaceID, shortCode)
	ps := s.client.rdb.Subscribe(ctx, channel)
	if _, err := ps.Receive(ctx); err != nil {
		_ = ps.Close()
		return nil, fmt.Errorf("clicksub: subscribing to %s: %w", channel, err)
	}

	return &ClickSubscription{ps: ps, channel: ps.Channel()}, nil
}

// SubscribeToWorkspace subscribes to all click channels for a workspace.
func (s *ClickSubscriber) SubscribeToWorkspace(
	ctx context.Context,
	workspaceID string,
) (ClickEventStream, error) {
	pattern := analytics.WorkspacePattern(workspaceID)
	ps := s.client.rdb.PSubscribe(ctx, pattern)
	if _, err := ps.Receive(ctx); err != nil {
		_ = ps.Close()
		return nil, fmt.Errorf("clicksub: psubscribing to %s: %w", pattern, err)
	}

	return &ClickSubscription{ps: ps, channel: ps.Channel()}, nil
}
