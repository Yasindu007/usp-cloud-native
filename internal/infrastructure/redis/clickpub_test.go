//go:build integration
// +build integration

package redis_test

import (
	"context"
	"testing"
	"time"

	"github.com/urlshortener/platform/internal/domain/analytics"
	redisinfra "github.com/urlshortener/platform/internal/infrastructure/redis"
)

func TestClickPublisherAndSubscriber_URL(t *testing.T) {
	client := testClient(t)
	pub := redisinfra.NewClickPublisher(client)
	sub := redisinfra.NewClickSubscriber(client)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := sub.SubscribeToURL(ctx, "ws_001", "abc1234")
	if err != nil {
		t.Fatalf("SubscribeToURL failed: %v", err)
	}
	defer stream.Close()

	evt := &analytics.ClickEvent{
		ShortCode:   "abc1234",
		WorkspaceID: "ws_001",
		OccurredAt:  time.Now().UTC(),
		DeviceType:  string(analytics.DeviceTypeDesktop),
		CountryCode: "XX",
		RequestID:   "req_001",
	}
	if err := pub.Publish(ctx, evt); err != nil {
		t.Fatalf("Publish failed: %v", err)
	}

	select {
	case got := <-stream.Events():
		if got == nil || got.ShortCode != evt.ShortCode || got.WorkspaceID != evt.WorkspaceID {
			t.Fatalf("unexpected event: %+v", got)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for click event")
	}
}

func TestClickSubscriber_WorkspacePattern(t *testing.T) {
	client := testClient(t)
	pub := redisinfra.NewClickPublisher(client)
	sub := redisinfra.NewClickSubscriber(client)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := sub.SubscribeToWorkspace(ctx, "ws_001")
	if err != nil {
		t.Fatalf("SubscribeToWorkspace failed: %v", err)
	}
	defer stream.Close()

	evt := &analytics.ClickEvent{
		ShortCode:   "xyz9876",
		WorkspaceID: "ws_001",
		OccurredAt:  time.Now().UTC(),
		DeviceType:  string(analytics.DeviceTypeMobile),
		CountryCode: "XX",
		RequestID:   "req_002",
	}
	if err := pub.Publish(ctx, evt); err != nil {
		t.Fatalf("Publish failed: %v", err)
	}

	select {
	case got := <-stream.Events():
		if got == nil || got.ShortCode != evt.ShortCode {
			t.Fatalf("unexpected event: %+v", got)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for workspace click event")
	}
}
