// Package analytics provides the analytics event ingestion service.
//
// Architecture: producer-consumer with bounded channel + batch flusher
//
//	Redirect Handler           Ingestion Service          PostgreSQL
//	─────────────              ─────────────────          ──────────
//	302 response sent  ──►     channel <- event ──►      WriteMany()
//	(non-blocking)             (background drain)         IncrementClickCounts()
//
// Why two separate DB operations per flush?
//  1. WriteMany: inserts raw events into redirect_events (analytics truth)
//  2. IncrementClickCounts: updates urls.click_count (fast read counter)
//     They run sequentially in the same flush cycle. If either fails,
//     the error is logged and the dropped-events counter is incremented.
//     A reconciliation job (Phase 4) periodically recomputes click_count
//     from redirect_events to correct any drift from failed increments.
//
// Event construction:
//
//	Events are fully constructed by Capture() before entering the channel.
//	No further enrichment happens inside the drainer — the drainer only
//	writes what it receives. This keeps the drainer simple and testable.
package analytics

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/urlshortener/platform/internal/domain/analytics"
	"github.com/urlshortener/platform/pkg/geoip"
	"github.com/urlshortener/platform/pkg/iphasher"
	"github.com/urlshortener/platform/pkg/useragent"
)

const (
	// maxChannelSize is the bounded channel capacity.
	// At 10k RPS, this gives ~200ms of buffer before dropping starts.
	// Sized conservatively — the batch writer should drain faster than events arrive.
	maxChannelSize = 2048

	// defaultBatchSize is events to accumulate before flushing.
	defaultBatchSize = 100

	// defaultFlushInterval is the max time between flushes.
	// Balances latency (events visible in analytics) vs. overhead (DB round-trips).
	// 100ms means analytics are at most 100ms behind real time.
	defaultFlushInterval = 100 * time.Millisecond
)

// Writer is the storage interface for analytics events.
// Implemented by postgres.AnalyticsRepository.
type Writer interface {
	WriteMany(ctx context.Context, events []*analytics.RedirectEvent) error
	IncrementClickCounts(ctx context.Context, counts map[string]int64) error
}

// Service manages analytics event capture, enrichment, and persistence.
type Service struct {
	writer        Writer
	hasher        *iphasher.Hasher
	channel       chan *analytics.RedirectEvent
	batchSize     int
	flushInterval time.Duration
	log           *slog.Logger
	stopDrainer   context.CancelFunc
	drainerDone   chan struct{}

	// droppedEvents tracks events lost due to channel saturation.
	droppedEvents atomic.Int64
	// writtenEvents tracks events successfully persisted.
	writtenEvents atomic.Int64
}

// NewService creates and starts an analytics ingestion Service.
// The background drainer goroutine starts immediately and runs until
// ctx is cancelled. Call Shutdown() during graceful shutdown to flush
// any buffered events before process exit.
func NewService(ctx context.Context, writer Writer, hasher *iphasher.Hasher, log *slog.Logger) *Service {
	drainerCtx, cancel := context.WithCancel(ctx)
	s := &Service{
		writer:        writer,
		hasher:        hasher,
		channel:       make(chan *analytics.RedirectEvent, maxChannelSize),
		batchSize:     defaultBatchSize,
		flushInterval: defaultFlushInterval,
		log:           log,
		stopDrainer:   cancel,
		drainerDone:   make(chan struct{}),
	}
	go s.runDrainer(drainerCtx)
	return s
}

// CaptureRequest is the input to the analytics capture pipeline.
// Populated from the HTTP request in the redirect handler.
// Using a dedicated struct (not individual parameters) keeps the
// Capture call site clean and makes the input contract explicit.
type CaptureRequest struct {
	ShortCode    string
	WorkspaceID  string
	RawIP        string // raw client IP — hashed before storage
	UserAgentRaw string
	ReferrerRaw  string
	RequestID    string
	OccurredAt   time.Time
}

// Capture builds a fully-enriched RedirectEvent and enqueues it for
// async persistence. Returns immediately — never blocks the caller.
//
// Enrichment performed synchronously here (before enqueueing):
//   - UA parsing (device type, browser, OS, bot detection) ~200ns
//   - IP hashing (SHA-256 with daily salt) ~500ns
//   - GeoIP lookup (stub: ~10ns; Phase 4 MaxMind: ~500ns)
//   - Referrer domain extraction ~100ns
//     Total: ~800ns per redirect — well within the 50ms P99 SLO budget
//
// Why enrich synchronously (not in the drainer)?
//
//	The daily IP salt is time-sensitive — if events sit in the channel
//	for >1ms, the salt doesn't change (it's daily), so timing is not the
//	concern. The real reason is testability: enriched events are easier
//	to assert in unit tests than raw CaptureRequests.
func (s *Service) Capture(req CaptureRequest) {
	parsedUA := useragent.Parse(req.UserAgentRaw)

	var ipHash string
	if !parsedUA.IsBot && req.RawIP != "" {
		ipHash = s.hasher.Hash(req.RawIP)
	}

	countryCode := geoip.Lookup(req.RawIP)

	referrerDomain := useragent.ExtractReferrerDomain(req.ReferrerRaw)

	occurredAt := req.OccurredAt
	if occurredAt.IsZero() {
		occurredAt = time.Now().UTC()
	}

	// Truncate referrer to bound storage size
	referrerRaw := req.ReferrerRaw
	if len(referrerRaw) > 1024 {
		referrerRaw = referrerRaw[:1024]
	}

	// Truncate UA to bound storage size
	userAgent := req.UserAgentRaw
	if len(userAgent) > 512 {
		userAgent = userAgent[:512]
	}

	evt := &analytics.RedirectEvent{
		ID:             ulid.Make().String(),
		ShortCode:      req.ShortCode,
		WorkspaceID:    req.WorkspaceID,
		OccurredAt:     occurredAt,
		IPHash:         ipHash,
		UserAgent:      userAgent,
		DeviceType:     analytics.DeviceType(parsedUA.DeviceType),
		BrowserFamily:  parsedUA.BrowserFamily,
		OSFamily:       parsedUA.OSFamily,
		IsBot:          parsedUA.IsBot,
		CountryCode:    countryCode,
		ReferrerDomain: referrerDomain,
		ReferrerRaw:    referrerRaw,
		RequestID:      req.RequestID,
	}

	select {
	case s.channel <- evt:
		// Enqueued successfully.
	default:
		// Channel full — drop event rather than blocking the redirect response.
		s.droppedEvents.Add(1)
		s.log.Warn("analytics event dropped: channel full",
			slog.String("short_code", req.ShortCode),
			slog.Int64("total_dropped", s.droppedEvents.Load()),
		)
	}
}

// DroppedEvents returns the count of events dropped due to channel saturation.
// Exposed for Prometheus metric scraping.
func (s *Service) DroppedEvents() int64 { return s.droppedEvents.Load() }

// WrittenEvents returns the count of events successfully persisted.
func (s *Service) WrittenEvents() int64 { return s.writtenEvents.Load() }

// Shutdown drains the channel and persists any remaining buffered events.
// Must be called during graceful shutdown before closing the DB connection.
// The ctx controls the maximum time allowed for the final flush.
func (s *Service) Shutdown(ctx context.Context) {
	if s.stopDrainer != nil {
		s.stopDrainer()
	}
	select {
	case <-s.drainerDone:
	case <-ctx.Done():
		return
	}

	var remaining []*analytics.RedirectEvent
	for {
		select {
		case evt := <-s.channel:
			remaining = append(remaining, evt)
		default:
			goto flush
		}
	}
flush:
	if len(remaining) == 0 {
		return
	}
	s.log.Info("analytics service: flushing remaining events on shutdown",
		slog.Int("count", len(remaining)),
	)
	s.flush(ctx, remaining)
}

// runDrainer is the background goroutine that batches and persists events.
func (s *Service) runDrainer(ctx context.Context) {
	defer close(s.drainerDone)

	ticker := time.NewTicker(s.flushInterval)
	defer ticker.Stop()

	batch := make([]*analytics.RedirectEvent, 0, s.batchSize)

	for {
		select {
		case <-ctx.Done():
			// Flush in-memory batch that may have been drained from the channel
			// but not yet persisted.
			if len(batch) > 0 {
				s.flush(context.Background(), batch)
			}
			return

		case evt := <-s.channel:
			batch = append(batch, evt)
			if len(batch) >= s.batchSize {
				s.flush(context.Background(), batch)
				batch = batch[:0]
			}

		case <-ticker.C:
			if len(batch) > 0 {
				s.flush(context.Background(), batch)
				batch = batch[:0]
			}
		}
	}
}

// flush writes a batch of events and increments click counts.
// All errors are logged — never returned to the caller (fire-and-forget).
func (s *Service) flush(ctx context.Context, batch []*analytics.RedirectEvent) {
	if len(batch) == 0 {
		return
	}

	writeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Step 1: Write redirect events to the analytics table.
	if err := s.writer.WriteMany(writeCtx, batch); err != nil {
		s.log.Error("analytics: batch write failed",
			slog.String("error", err.Error()),
			slog.Int("batch_size", len(batch)),
		)
		s.droppedEvents.Add(int64(len(batch)))
		return
	}

	s.writtenEvents.Add(int64(len(batch)))

	// Step 2: Aggregate click counts per short_code and update urls table.
	// We aggregate here (not in the drainer) to reduce the number of UPDATE statements.
	counts := make(map[string]int64, len(batch))
	for _, evt := range batch {
		if !evt.IsBot {
			// Only count non-bot redirects in click_count.
			// Bot traffic is stored in redirect_events but not counted.
			counts[evt.ShortCode]++
		}
	}

	if err := s.writer.IncrementClickCounts(writeCtx, counts); err != nil {
		// Non-fatal: events are written, click_count will be reconciled.
		s.log.Warn("analytics: click count increment failed",
			slog.String("error", err.Error()),
			slog.Int("unique_codes", len(counts)),
		)
	}
}
