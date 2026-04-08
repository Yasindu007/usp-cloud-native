package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	domainaudit "github.com/urlshortener/platform/internal/domain/audit"
)

const auditTracerName = "github.com/urlshortener/platform/internal/infrastructure/postgres/audit"

// AuditRepository persists audit events to the audit_logs table.
//
// Write path design:
//
//	All audit writes use the PRIMARY pool because:
//	1. Audit events must be immediately durable — a replica lag of
//	   even 50ms means an event could be lost if the primary fails
//	   between the INSERT and replication.
//	2. Audit events are written once (INSERT only) — there is no
//	   read-after-write concern to justify replica use.
//
// Batch writes:
//
//	The WriteMany method accepts a slice of events and inserts them in a
//	single multi-value INSERT statement. The audit service calls this
//	from a background goroutine that drains its buffer every N events
//	or every T milliseconds (whichever comes first). This amortises the
//	per-roundtrip overhead across many events.
//
// Error handling:
//
//	Audit write failures are logged but never returned to the caller
//	as application errors. A failed audit write must not cause a
//	business operation to fail — the user's URL was still created.
//	This is the "best-effort audit" model documented in the PRD.
type AuditRepository struct {
	db *Client
}

// NewAuditRepository creates an AuditRepository.
func NewAuditRepository(db *Client) *AuditRepository {
	return &AuditRepository{db: db}
}

// Write persists a single audit event. Used for low-frequency events.
// For high-frequency paths (URL shortening), use WriteMany via the service.
func (r *AuditRepository) Write(ctx context.Context, evt *domainaudit.Event) error {
	ctx, span := otel.Tracer(auditTracerName).Start(ctx, "AuditRepository.Write",
		trace.WithAttributes(
			attribute.String("audit.action", string(evt.Action)),
		),
	)
	defer span.End()

	metadataJSON, err := marshalMetadata(evt.Metadata)
	if err != nil {
		return fmt.Errorf("audit: marshaling metadata: %w", err)
	}

	const query = `
		INSERT INTO audit_logs (
			id, workspace_id, actor_id, actor_type,
			action, resource_type, resource_id,
			source_ip, user_agent, request_id,
			metadata, occurred_at
		) VALUES (
			$1, $2, $3, $4,
			$5, $6, $7,
			$8, $9, $10,
			$11, $12
		)`

	_, err = r.db.Primary().Exec(ctx, query,
		evt.ID,
		evt.WorkspaceID, // nil → NULL (pointer)
		evt.ActorID,
		string(evt.ActorType),
		string(evt.Action),
		string(evt.ResourceType),
		evt.ResourceID,
		nilIfEmpty(evt.SourceIP),
		nilIfEmpty(evt.UserAgent),
		nilIfEmpty(evt.RequestID),
		metadataJSON, // nil when no metadata
		evt.OccurredAt,
	)
	if err != nil {
		span.RecordError(err)
		return fmt.Errorf("audit: inserting event: %w", err)
	}

	return nil
}

// WriteMany inserts multiple audit events in a single database round-trip
// using a multi-value INSERT. This is the preferred write path for the
// async batch writer in the audit service.
//
// PostgreSQL handles up to ~65535 parameters per query. At 12 columns
// per event: max 5461 events per batch. The audit service caps batches
// at 500 events, well within this limit.
//
// On any error: all events in the batch are lost (no partial retry).
// The service logs the error — individual event loss is acceptable per
// the "best-effort audit" model. For higher durability, swap this with
// a per-event retry queue (Phase 3).
func (r *AuditRepository) WriteMany(ctx context.Context, events []*domainaudit.Event) error {
	if len(events) == 0 {
		return nil
	}

	ctx, span := otel.Tracer(auditTracerName).Start(ctx, "AuditRepository.WriteMany",
		trace.WithAttributes(
			attribute.Int("audit.batch_size", len(events)),
		),
	)
	defer span.End()

	// Build a parameterised multi-value INSERT.
	// Each event contributes 12 parameter placeholders.
	// $1...$12 for event[0], $13...$24 for event[1], etc.
	const colsPerRow = 12
	args := make([]any, 0, len(events)*colsPerRow)
	valuesClauses := make([]string, 0, len(events))

	for i, evt := range events {
		base := i * colsPerRow
		metadataJSON, _ := marshalMetadata(evt.Metadata)

		valuesClauses = append(valuesClauses, fmt.Sprintf(
			"($%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d)",
			base+1, base+2, base+3, base+4,
			base+5, base+6, base+7,
			base+8, base+9, base+10,
			base+11, base+12,
		))

		args = append(args,
			evt.ID,
			evt.WorkspaceID,
			evt.ActorID,
			string(evt.ActorType),
			string(evt.Action),
			string(evt.ResourceType),
			evt.ResourceID,
			nilIfEmpty(evt.SourceIP),
			nilIfEmpty(evt.UserAgent),
			nilIfEmpty(evt.RequestID),
			metadataJSON,
			evt.OccurredAt,
		)
	}

	query := `
		INSERT INTO audit_logs (
			id, workspace_id, actor_id, actor_type,
			action, resource_type, resource_id,
			source_ip, user_agent, request_id,
			metadata, occurred_at
		) VALUES ` + joinStrings(valuesClauses, ", ")

	_, err := r.db.Primary().Exec(ctx, query, args...)
	if err != nil {
		span.RecordError(err)
		return fmt.Errorf("audit: batch insert %d events: %w", len(events), err)
	}

	return nil
}

// ListByActor returns audit events for a specific actor (compliance queries).
// Not in the hot path — used by admin dashboards and compliance exports.
func (r *AuditRepository) ListByActor(ctx context.Context, actorID string, limit int) ([]*domainaudit.Event, error) {
	const query = `
		SELECT id, workspace_id, actor_id, actor_type, action,
		       resource_type, resource_id, source_ip, user_agent,
		       request_id, metadata, occurred_at
		FROM audit_logs
		WHERE actor_id = $1
		ORDER BY occurred_at DESC
		LIMIT $2`

	rows, err := r.db.Replica().Query(ctx, query, actorID, limit)
	if err != nil {
		return nil, fmt.Errorf("audit: querying by actor: %w", err)
	}
	defer rows.Close()

	var events []*domainaudit.Event
	for rows.Next() {
		evt, err := scanAuditEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("audit: scanning row: %w", err)
		}
		events = append(events, evt)
	}
	return events, rows.Err()
}

// ListByWorkspace returns audit events for a workspace in reverse chronological order.
func (r *AuditRepository) ListByWorkspace(ctx context.Context, workspaceID string, limit int) ([]*domainaudit.Event, error) {
	const query = `
		SELECT id, workspace_id, actor_id, actor_type, action,
		       resource_type, resource_id, source_ip, user_agent,
		       request_id, metadata, occurred_at
		FROM audit_logs
		WHERE workspace_id = $1
		ORDER BY occurred_at DESC
		LIMIT $2`

	rows, err := r.db.Replica().Query(ctx, query, workspaceID, limit)
	if err != nil {
		return nil, fmt.Errorf("audit: querying by workspace: %w", err)
	}
	defer rows.Close()

	var events []*domainaudit.Event
	for rows.Next() {
		evt, err := scanAuditEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("audit: scanning row: %w", err)
		}
		events = append(events, evt)
	}
	return events, rows.Err()
}

// ── Scanning ──────────────────────────────────────────────────────────────────

type auditRowScanner interface {
	Scan(dest ...any) error
}

func scanAuditEvent(row auditRowScanner) (*domainaudit.Event, error) {
	var evt domainaudit.Event
	var actorType, action, resourceType string
	var metadataRaw []byte
	var sourceIP, userAgent, requestID *string

	err := row.Scan(
		&evt.ID,
		&evt.WorkspaceID,
		&evt.ActorID,
		&actorType,
		&action,
		&resourceType,
		&evt.ResourceID,
		&sourceIP,
		&userAgent,
		&requestID,
		&metadataRaw,
		&evt.OccurredAt,
	)
	if err != nil {
		return nil, err
	}

	evt.ActorType = domainaudit.ActorType(actorType)
	evt.Action = domainaudit.Action(action)
	evt.ResourceType = domainaudit.ResourceType(resourceType)

	if sourceIP != nil {
		evt.SourceIP = *sourceIP
	}
	if userAgent != nil {
		evt.UserAgent = *userAgent
	}
	if requestID != nil {
		evt.RequestID = *requestID
	}

	if metadataRaw != nil {
		if err := json.Unmarshal(metadataRaw, &evt.Metadata); err != nil {
			evt.Metadata = map[string]any{"_raw": string(metadataRaw)}
		}
	}

	return &evt, nil
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// marshalMetadata converts metadata to JSON bytes for storage.
// Returns nil (not an empty JSON object) when metadata is nil or empty —
// this stores NULL in the database, saving space for events without metadata.
func marshalMetadata(m map[string]any) ([]byte, error) {
	if len(m) == 0 {
		return nil, nil
	}
	return json.Marshal(m)
}

// joinStrings joins string slices with a separator.
// Avoids importing "strings" for a single use.
func joinStrings(ss []string, sep string) string {
	if len(ss) == 0 {
		return ""
	}
	result := ss[0]
	for _, s := range ss[1:] {
		result += sep + s
	}
	return result
}
