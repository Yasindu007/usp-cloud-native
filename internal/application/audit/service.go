// Package audit provides the audit logging application service.
//
// The service sits between the HTTP middleware (which captures events)
// and the audit repository (which persists them). Its primary function
// is to buffer events and write them in batches to reduce DB load.
//
// Architecture: producer-consumer with bounded channel
//
//	HTTP Middleware          Audit Service         PostgreSQL
//	─────────────           ─────────────         ──────────
//	Request handled  ──►    channel <- event  ──► WriteMany()
//	(non-blocking send)     (background drain)
//
// The channel is bounded (capacity = maxChannelSize).
// If the channel is full (write lag or DB overload), events are dropped
// rather than blocking the HTTP request. This is the "best-effort audit"
// model — reliability of business operations takes priority over audit
// completeness. A counter tracks dropped events for alerting.
//
// Batching strategy:
//
//	The background goroutine waits for either:
//	a) batchSize events to accumulate, OR
//	b) flushInterval to elapse (whichever comes first)
//	This bounds both latency (events are written within flushInterval)
//	and overhead (writes are batched, not per-event).
package audit

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/oklog/ulid/v2"

	domainaudit "github.com/urlshortener/platform/internal/domain/audit"
	domainauth "github.com/urlshortener/platform/internal/domain/auth"
)

// Writer is the interface for persisting audit events.
// Implemented by postgres.AuditRepository.
type Writer interface {
	Write(ctx context.Context, evt *domainaudit.Event) error
	WriteMany(ctx context.Context, events []*domainaudit.Event) error
}

const (
	// maxChannelSize is the bounded buffer capacity.
	// At 1000 events/s, 2048 gives ~2s of buffer before dropping starts.
	maxChannelSize = 2048

	// defaultBatchSize is the number of events to accumulate before writing.
	defaultBatchSize = 50

	// defaultFlushInterval is the maximum time between writes.
	// Guarantees events are persisted within this window even during low traffic.
	defaultFlushInterval = 500 * time.Millisecond
)

// Service manages asynchronous audit event capture and persistence.
type Service struct {
	writer        Writer
	channel       chan *domainaudit.Event
	batchSize     int
	flushInterval time.Duration
	log           *slog.Logger

	// droppedEvents counts events lost because the channel was full.
	// Exposed via DroppedEvents() for Prometheus metric scraping.
	droppedEvents atomic.Int64
}

// NewService creates and starts an audit Service.
// The background goroutine starts immediately and runs until ctx is cancelled.
//
// Callers must call Shutdown() (or cancel the context) at application exit
// to flush any buffered events before the process terminates.
func NewService(ctx context.Context, writer Writer, log *slog.Logger) *Service {
	s := &Service{
		writer:        writer,
		channel:       make(chan *domainaudit.Event, maxChannelSize),
		batchSize:     defaultBatchSize,
		flushInterval: defaultFlushInterval,
		log:           log,
	}
	go s.runDrainer(ctx)
	return s
}

// Capture enqueues an audit event for async persistence.
// Never blocks the caller — if the channel is full, the event is dropped
// and the dropped counter is incremented.
//
// Called by the audit HTTP middleware after each request completes.
func (s *Service) Capture(evt *domainaudit.Event) {
	select {
	case s.channel <- evt:
		// Event enqueued successfully.
	default:
		// Channel full — drop event to avoid blocking the HTTP response.
		s.droppedEvents.Add(1)
		s.log.Warn("audit event dropped: channel full",
			slog.String("action", string(evt.Action)),
			slog.String("actor_id", evt.ActorID),
		)
	}
}

// BuildEvent constructs a complete audit event from request context.
// Called by the audit middleware after a request completes.
//
// Identity is extracted from JWT/API key claims (set by auth middleware).
// If no claims are present (unauthenticated request), a minimal event
// is still built — audit of unauthenticated actions is valid and required.
func (s *Service) BuildEvent(
	ctx context.Context,
	action domainaudit.Action,
	resourceType domainaudit.ResourceType,
	resourceID string,
	sourceIP, userAgent, requestID string,
	metadata map[string]any,
) *domainaudit.Event {
	actorID := "anonymous"
	actorType := domainaudit.ActorSystem
	var workspaceID *string

	if claims, ok := domainauth.FromContext(ctx); ok {
		actorID = claims.UserID
		actorType = resolveActorType(claims)
		if claims.WorkspaceID != "" {
			ws := claims.WorkspaceID
			workspaceID = &ws
		}
	}

	return &domainaudit.Event{
		ID:           ulid.Make().String(),
		WorkspaceID:  workspaceID,
		ActorID:      actorID,
		ActorType:    actorType,
		Action:       action,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		SourceIP:     sourceIP,
		UserAgent:    userAgent,
		RequestID:    requestID,
		Metadata:     metadata,
		OccurredAt:   time.Now().UTC(),
	}
}

// DroppedEvents returns the total number of audit events dropped due to
// channel saturation. Used by Prometheus metric scraping.
func (s *Service) DroppedEvents() int64 {
	return s.droppedEvents.Load()
}

// Shutdown drains the channel and writes any remaining buffered events.
// Must be called during graceful shutdown before the process exits.
// Pass a context with a timeout to bound the drain duration.
func (s *Service) Shutdown(ctx context.Context) {
	// Drain remaining events from the channel.
	var remaining []*domainaudit.Event
	for {
		select {
		case evt := <-s.channel:
			remaining = append(remaining, evt)
		default:
			// Channel empty — proceed to flush.
			goto flush
		}
	}

flush:
	if len(remaining) == 0 {
		return
	}

	s.log.Info("audit service: flushing remaining events on shutdown",
		slog.Int("count", len(remaining)),
	)

	if err := s.writer.WriteMany(ctx, remaining); err != nil {
		s.log.Error("audit service: failed to flush events on shutdown",
			slog.String("error", err.Error()),
			slog.Int("lost_events", len(remaining)),
		)
	}
}

// runDrainer is the background goroutine that batches and writes events.
// Runs until ctx is cancelled.
func (s *Service) runDrainer(ctx context.Context) {
	ticker := time.NewTicker(s.flushInterval)
	defer ticker.Stop()

	batch := make([]*domainaudit.Event, 0, s.batchSize)

	flush := func() {
		if len(batch) == 0 {
			return
		}
		// Use a background context for the write — the request contexts
		// that produced these events are long gone.
		writeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if err := s.writer.WriteMany(writeCtx, batch); err != nil {
			s.log.Error("audit service: batch write failed",
				slog.String("error", err.Error()),
				slog.Int("batch_size", len(batch)),
			)
			// Increment dropped counter for the failed batch.
			s.droppedEvents.Add(int64(len(batch)))
		}
		// Reset batch — reuse underlying array to reduce allocations.
		batch = batch[:0]
	}

	for {
		select {
		case <-ctx.Done():
			// Context cancelled (application shutting down).
			// Final flush is handled by Shutdown() — do not double-write here.
			return

		case evt := <-s.channel:
			batch = append(batch, evt)
			// Flush when batch is full to bound latency.
			if len(batch) >= s.batchSize {
				flush()
			}

		case <-ticker.C:
			// Time-based flush — guarantees events are written
			// within flushInterval even during low traffic.
			flush()
		}
	}
}

// resolveActorType determines whether the authenticated caller is a user
// or an API key, based on the claims issuer field.
// API key claims have Issuer="apikey" (set by APIKeyAuth middleware).
// JWT claims have Issuer="http://localhost:9000" (or the real issuer URL).
func resolveActorType(claims *domainauth.Claims) domainaudit.ActorType {
	if claims.Issuer == "apikey" {
		return domainaudit.ActorAPIKey
	}
	return domainaudit.ActorUser
}
