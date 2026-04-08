package audit_test

import (
	"context"
	"testing"
	"time"

	domainaudit "github.com/urlshortener/platform/internal/domain/audit"
)

func TestWithPendingEvent_And_PendingEventFromContext(t *testing.T) {
	evt := &domainaudit.Event{
		ID:           "01HTEST",
		ActorID:      "usr_001",
		ActorType:    domainaudit.ActorUser,
		Action:       domainaudit.ActionURLCreate,
		ResourceType: domainaudit.ResourceURL,
		ResourceID:   "url_001",
		OccurredAt:   time.Now().UTC(),
	}

	ctx := domainaudit.WithPendingEvent(context.Background(), evt)

	retrieved, ok := domainaudit.PendingEventFromContext(ctx)
	if !ok {
		t.Fatal("expected pending event in context, got none")
	}
	if retrieved.ID != "01HTEST" {
		t.Errorf("expected ID=01HTEST, got %q", retrieved.ID)
	}
	if retrieved.Action != domainaudit.ActionURLCreate {
		t.Errorf("expected action=url:create, got %q", retrieved.Action)
	}
}

func TestPendingEventFromContext_EmptyContext_ReturnsFalse(t *testing.T) {
	_, ok := domainaudit.PendingEventFromContext(context.Background())
	if ok {
		t.Error("expected false for empty context, got true")
	}
}

func TestAnnotateContext_UpdatesEventFields(t *testing.T) {
	evt := &domainaudit.Event{
		ID:        "01HTEST",
		ActorID:   "usr_001",
		ActorType: domainaudit.ActorUser,
		Action:    domainaudit.ActionURLCreate,
	}

	ctx := domainaudit.WithPendingEvent(context.Background(), evt)

	domainaudit.AnnotateContext(ctx, domainaudit.ResourceURL, "url_xyz", map[string]any{
		"short_code": "abc1234",
	})

	retrieved, _ := domainaudit.PendingEventFromContext(ctx)

	if retrieved.ResourceType != domainaudit.ResourceURL {
		t.Errorf("expected ResourceType=url, got %q", retrieved.ResourceType)
	}
	if retrieved.ResourceID != "url_xyz" {
		t.Errorf("expected ResourceID=url_xyz, got %q", retrieved.ResourceID)
	}
	if retrieved.Metadata["short_code"] != "abc1234" {
		t.Errorf("expected Metadata[short_code]=abc1234, got %v", retrieved.Metadata["short_code"])
	}
}

func TestAnnotateContext_NoOpWhenNoPendingEvent(t *testing.T) {
	// Must not panic — handlers call this unconditionally
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("AnnotateContext panicked: %v", r)
		}
	}()

	domainaudit.AnnotateContext(context.Background(),
		domainaudit.ResourceURL, "url_001", nil)
}

func TestAnnotateContext_NilMetadata_DoesNotClearExisting(t *testing.T) {
	evt := &domainaudit.Event{
		Metadata: map[string]any{"existing_key": "existing_val"},
	}
	ctx := domainaudit.WithPendingEvent(context.Background(), evt)

	// Pass nil metadata — existing metadata must be preserved
	domainaudit.AnnotateContext(ctx, domainaudit.ResourceURL, "url_001", nil)

	retrieved, _ := domainaudit.PendingEventFromContext(ctx)
	if retrieved.Metadata["existing_key"] != "existing_val" {
		t.Error("nil metadata in AnnotateContext must not clear existing metadata")
	}
}

func TestActionConstants_AreHumanReadable(t *testing.T) {
	// Verify action strings are what compliance engineers expect to see in DB
	cases := map[domainaudit.Action]string{
		domainaudit.ActionURLCreate:       "url:create",
		domainaudit.ActionURLDelete:       "url:delete",
		domainaudit.ActionMemberAdd:       "member:add",
		domainaudit.ActionAPIKeyCreate:    "apikey:create",
		domainaudit.ActionAPIKeyRevoke:    "apikey:revoke",
		domainaudit.ActionWorkspaceCreate: "workspace:create",
	}
	for action, expected := range cases {
		if string(action) != expected {
			t.Errorf("action %q: expected %q", action, expected)
		}
	}
}
