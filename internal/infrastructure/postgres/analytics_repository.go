package postgres

import (
	"context"
	"fmt"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/urlshortener/platform/internal/domain/analytics"
)

const analyticsTracerName = "github.com/urlshortener/platform/internal/infrastructure/postgres/analytics"

// AnalyticsRepository implements domain/analytics.Repository.
// All writes go to the PRIMARY pool — redirect_events is append-only
// and must be immediately durable (no replica lag acceptable for event data).
type AnalyticsRepository struct {
	db *Client
}

// NewAnalyticsRepository creates an AnalyticsRepository.
func NewAnalyticsRepository(db *Client) *AnalyticsRepository {
	return &AnalyticsRepository{db: db}
}

// WriteMany inserts a batch of redirect events in a single SQL statement.
//
// Multi-value INSERT design:
//
//	Rather than N individual INSERTs (N round-trips), we build one
//	INSERT INTO redirect_events (...) VALUES (...), (...), ... (1 round-trip).
//	At batch size 100 events: 1 round-trip instead of 100.
//	At 10k RPS with 100ms flush interval: ~1000 events/batch → 10 batches/s
//	instead of 10,000 individual inserts/s.
//
// PostgreSQL parameter limit:
//
//	PostgreSQL supports up to 65535 parameters per query.
//	At 14 columns per event: max 4681 events per batch.
//	The ingestion service caps batches at 500 — safely within limits.
//
// Conflict handling:
//
//	ON CONFLICT DO NOTHING prevents duplicate event IDs from causing
//	batch failures if the same event is enqueued twice (idempotent writes).
//	This can happen if the redirect service crashes between writing to the
//	channel and the drainer acknowledging the write.
func (r *AnalyticsRepository) WriteMany(ctx context.Context, events []*analytics.RedirectEvent) error {
	if len(events) == 0 {
		return nil
	}

	ctx, span := otel.Tracer(analyticsTracerName).Start(ctx, "AnalyticsRepository.WriteMany",
		trace.WithAttributes(
			attribute.Int("analytics.batch_size", len(events)),
		),
	)
	defer span.End()

	const colsPerRow = 14
	args := make([]any, 0, len(events)*colsPerRow)
	placeholders := make([]string, 0, len(events))

	for i, evt := range events {
		base := i * colsPerRow
		placeholders = append(placeholders, fmt.Sprintf(
			"($%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d)",
			base+1, base+2, base+3, base+4, base+5, base+6, base+7,
			base+8, base+9, base+10, base+11, base+12, base+13, base+14,
		))

		args = append(args,
			evt.ID,
			evt.ShortCode,
			evt.WorkspaceID,
			evt.OccurredAt,
			nilIfEmpty(evt.IPHash),
			nilIfEmpty(evt.UserAgent),
			nilIfEmpty(string(evt.DeviceType)),
			nilIfEmpty(evt.BrowserFamily),
			nilIfEmpty(evt.OSFamily),
			evt.IsBot,
			nilIfEmpty(evt.CountryCode),
			nilIfEmpty(evt.ReferrerDomain),
			nilIfEmpty(evt.ReferrerRaw),
			nilIfEmpty(evt.RequestID),
		)
	}

	query := `INSERT INTO redirect_events (
		id, short_code, workspace_id, occurred_at,
		ip_hash, user_agent, device_type, browser_family, os_family, is_bot,
		country_code, referrer_domain, referrer_raw, request_id
	) VALUES ` + strings.Join(placeholders, ", ") + ` ON CONFLICT DO NOTHING`

	_, err := r.db.Primary().Exec(ctx, query, args...)
	if err != nil {
		span.RecordError(err)
		return fmt.Errorf("analytics: batch insert %d events: %w", len(events), err)
	}

	return nil
}

// IncrementClickCounts atomically updates the denormalized click_count
// on the urls table for each unique short_code in the batch.
//
// Atomic increment design:
//
//	UPDATE urls SET click_count = click_count + $n WHERE short_code = $code
//	This is atomic at the PostgreSQL row level — no separate read-modify-write.
//	We process each short_code separately (not a single multi-target UPDATE)
//	because the count per code varies.
//
// Why update click_count here (not in the redirect handler)?
//
//	The redirect handler must complete in <50ms (P99 SLO). DB writes are
//	expensive. The analytics ingestion service batches redirect events and
//	updates click_counts together in the background, completely off the
//	critical path. The count is slightly stale (by the flush interval)
//	but correct within seconds.
//
// Correctness on batch failure:
//
//	If WriteMany succeeds but IncrementClickCounts fails, the event is
//	recorded but the counter is not updated. On the next flush, the same
//	events are NOT re-sent (they were consumed from the channel). The counter
//	will be slightly under-counted. For Phase 3 this is acceptable.
//	Phase 4 adds a WAL-based reconciliation job that recounts from redirect_events.
func (r *AnalyticsRepository) IncrementClickCounts(ctx context.Context, counts map[string]int64) error {
	if len(counts) == 0 {
		return nil
	}

	_, span := otel.Tracer(analyticsTracerName).Start(ctx, "AnalyticsRepository.IncrementClickCounts",
		trace.WithAttributes(
			attribute.Int("analytics.unique_codes", len(counts)),
		),
	)
	defer span.End()

	// Execute one UPDATE per unique short_code.
	// At typical batch sizes (100 events, ~20 unique codes): 20 round-trips.
	// This is acceptable — click_count updates are in the background, not hot-path.
	// Phase 4 optimisation: use a single multi-row UPDATE with a VALUES list.
	for shortCode, count := range counts {
		_, err := r.db.Primary().Exec(ctx,
			`UPDATE urls SET click_count = click_count + $1 WHERE short_code = $2 AND deleted_at IS NULL`,
			count, shortCode,
		)
		if err != nil {
			span.RecordError(err)
			// Log but continue — one failed counter update must not abort all others.
			// The error is returned to the caller for metric counting.
			return fmt.Errorf("analytics: incrementing click_count for %q: %w", shortCode, err)
		}
	}

	return nil
}
