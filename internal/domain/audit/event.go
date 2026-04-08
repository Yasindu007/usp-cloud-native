// Package audit defines the domain model for immutable audit log events.
//
// Audit logging satisfies PRD section 10.5:
//
//	"All write operations must be recorded in an audit log with: actor ID,
//	 action, resource type, resource ID, timestamp, source IP."
//
// Design decisions:
//
//  1. Action vocabulary is finite and centralised here.
//     String constants prevent typos and make the action space discoverable.
//     A compliance engineer can grep for domainaudit.ActionURLDelete to find
//     every code path that records URL deletions.
//
//  2. Events are value objects — they have no identity beyond their content.
//     Once created, they are immutable (no setters, no pointer receivers that
//     mutate state). The database enforces immutability at the storage level.
//
//  3. Context-based enrichment pattern:
//     The audit service reads actor identity from the request context (set by
//     the auth middleware) and request metadata from the HTTP middleware. This
//     decouples audit capturing from the business logic — handlers don't need
//     to know they are being audited.
//
//  4. Metadata is flexible JSONB.
//     Actions have action-specific context (e.g., URL create includes
//     original_url and short_code; member add includes new role). We use
//     map[string]any serialised to JSONB rather than typed structs to avoid
//     a migration every time an action gains a new context field.
package audit

import (
	"context"
	"time"
)

// Action represents a recorded operation in the audit log.
// Using string constants (not iota) makes the action value human-readable
// in the database and in log aggregation tools (Loki, Elasticsearch).
type Action string

const (
	// URL actions
	ActionURLCreate  Action = "url:create"
	ActionURLUpdate  Action = "url:update"
	ActionURLDelete  Action = "url:delete"
	ActionURLDisable Action = "url:disable"

	// Workspace actions
	ActionWorkspaceCreate Action = "workspace:create"
	ActionWorkspaceDelete Action = "workspace:delete"

	// Member actions
	ActionMemberAdd    Action = "member:add"
	ActionMemberRemove Action = "member:remove"
	ActionMemberUpdate Action = "member:update_role"

	// API key actions
	ActionAPIKeyCreate Action = "apikey:create"
	ActionAPIKeyRevoke Action = "apikey:revoke"

	// Auth actions
	ActionTokenRevoke Action = "auth:token_revoke"
	ActionAuthFailed  Action = "auth:failed"
)

// ResourceType identifies the kind of entity affected by an action.
type ResourceType string

const (
	ResourceURL       ResourceType = "url"
	ResourceWorkspace ResourceType = "workspace"
	ResourceMember    ResourceType = "member"
	ResourceAPIKey    ResourceType = "api_key"
	ResourceToken     ResourceType = "token"
)

// ActorType identifies the kind of actor that performed the action.
type ActorType string

const (
	ActorUser   ActorType = "user"
	ActorAPIKey ActorType = "api_key"
	ActorSystem ActorType = "system"
)

// Event is an immutable audit log entry.
// Created once, never modified. Persisted to the audit_logs table.
type Event struct {
	// ID is the ULID for this event. ULIDs are time-ordered, enabling
	// chronological pagination of audit logs without a separate timestamp index.
	ID string

	// WorkspaceID is the workspace context for this event.
	// Nil for platform-level events (e.g. workspace creation).
	WorkspaceID *string

	// ActorID is the ULID of the user or API key that performed the action.
	// "system" for automated/background actions.
	ActorID string

	// ActorType distinguishes users from API keys from system processes.
	ActorType ActorType

	// Action is the verb describing what happened.
	Action Action

	// ResourceType is the kind of entity that was affected.
	ResourceType ResourceType

	// ResourceID is the ULID of the affected entity.
	ResourceID string

	// SourceIP is the client IP address that initiated the action.
	// Raw in Phase 2; hashed with daily salt in Phase 3 (GDPR compliance).
	SourceIP string

	// UserAgent is the client's User-Agent header.
	UserAgent string

	// RequestID is the X-Request-ID for correlating audit events with
	// application logs and distributed traces.
	RequestID string

	// Metadata contains action-specific context as key-value pairs.
	// Serialised to JSONB in the database.
	// Example for url:create: {"short_code": "abc1234", "original_url": "..."}
	// Example for member:add: {"role": "editor", "invited_by": "usr_..."}
	Metadata map[string]any

	// OccurredAt is the UTC timestamp when the event was created.
	// Set by the domain constructor — never by the caller.
	OccurredAt time.Time
}

// ── Context key for audit capture ─────────────────────────────────────────────

type contextKey string

const pendingEventKey contextKey = "audit_pending_event"

// WithPendingEvent stores a partially-built audit event in the request context.
// The audit middleware reads it at response time to complete and persist it.
//
// This pattern allows the HTTP handler to annotate the audit event with
// resource-specific details (resource_id, metadata) without needing a
// reference to the audit service directly.
//
// Usage in handlers:
//
//	audit.AnnotateContext(r.Context(), domainaudit.ResourceURL, result.ID,
//	    map[string]any{"short_code": result.ShortCode})
func WithPendingEvent(ctx context.Context, evt *Event) context.Context {
	return context.WithValue(ctx, pendingEventKey, evt)
}

// PendingEventFromContext retrieves the pending audit event from context.
// Returns (nil, false) if no event has been started.
func PendingEventFromContext(ctx context.Context) (*Event, bool) {
	evt, ok := ctx.Value(pendingEventKey).(*Event)
	if !ok || evt == nil {
		return nil, false
	}
	return evt, true
}

// AnnotateContext is the primary handler-facing API for audit logging.
// It enriches the pending audit event (started by the audit middleware)
// with resource-specific details from the handler's business result.
//
// Parameters:
//
//	ctx          — the request context (must contain a pending event)
//	resourceType — the type of resource created/updated/deleted
//	resourceID   — the ULID of the affected resource
//	metadata     — action-specific context (safe to be nil)
//
// If no pending event exists in context, this is a no-op.
// Handlers should not panic if audit capture is disabled.
func AnnotateContext(ctx context.Context, resourceType ResourceType, resourceID string, metadata map[string]any) {
	evt, ok := PendingEventFromContext(ctx)
	if !ok {
		return
	}
	evt.ResourceType = resourceType
	evt.ResourceID = resourceID
	if metadata != nil {
		evt.Metadata = metadata
	}
}
