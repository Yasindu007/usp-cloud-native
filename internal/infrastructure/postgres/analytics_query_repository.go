package postgres

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"

	"github.com/urlshortener/platform/internal/domain/analytics"
)

const analyticsQueryTracerName = "github.com/urlshortener/platform/internal/infrastructure/postgres/analytics"

// AnalyticsQueryRepository executes read-only analytics queries against
// the redirect_events table on the READ REPLICA.
//
// All queries filter WHERE is_bot = false by default — analytics exclude
// bot traffic per the PRD. Bot counts are available via the Summary.BotClicks
// field which runs a separate COUNT with is_bot = true.
//
// Performance characteristics:
//
//	The redirect_events table is partitioned by month. PostgreSQL prunes
//	irrelevant partitions when the WHERE clause on occurred_at allows it.
//	Always pass occurred_at range bounds so the planner prunes aggressively.
//
//	At 2.5M events/day (PRD capacity estimate), a 30-day query scans ~75M
//	rows. With the composite index (short_code, occurred_at DESC), a single
//	URL's 30-day query scans only that URL's rows — typically 10k–100k rows.
//
// date_trunc and generate_series:
//
//	Time-series bucketing uses PostgreSQL's date_trunc() function and a
//	generate_series() LEFT JOIN. The generate_series ensures zero-count
//	buckets appear in the result — without it, gaps in traffic produce
//	gaps in the time-series array, breaking dashboard charts.
type AnalyticsQueryRepository struct {
	db *Client
}

// NewAnalyticsQueryRepository creates an AnalyticsQueryRepository.
func NewAnalyticsQueryRepository(db *Client) *AnalyticsQueryRepository {
	return &AnalyticsQueryRepository{db: db}
}

// GetSummary returns aggregate click counts for a short code over a time window.
func (r *AnalyticsQueryRepository) GetSummary(
	ctx context.Context,
	shortCode string,
	start, end time.Time,
) (*analytics.Summary, error) {
	ctx, span := otel.Tracer(analyticsQueryTracerName).Start(ctx, "AnalyticsQuery.GetSummary",
		attribute.String("url.short_code", shortCode),
	)
	defer span.End()

	// Single query: human clicks + bot clicks in one pass using conditional aggregation.
	// This avoids two separate DB round-trips for the same time window.
	const query = `
		SELECT
			COUNT(*) FILTER (WHERE is_bot = false)                AS total_clicks,
			COUNT(DISTINCT ip_hash) FILTER (WHERE is_bot = false) AS unique_ips,
			COUNT(*) FILTER (WHERE is_bot = true)                 AS bot_clicks
		FROM redirect_events
		WHERE short_code = $1
		  AND occurred_at >= $2
		  AND occurred_at <  $3`

	var total, unique, bots int64
	err := r.db.Replica().QueryRow(ctx, query, shortCode, start, end).
		Scan(&total, &unique, &bots)
	if err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("analytics: summary query: %w", err)
	}

	span.SetAttributes(
		attribute.Int64("analytics.total_clicks", total),
	)

	return &analytics.Summary{
		ShortCode:   shortCode,
		TotalClicks: total,
		UniqueIPs:   unique,
		BotClicks:   bots,
		WindowStart: start,
		WindowEnd:   end,
	}, nil
}

// GetTimeSeries returns click counts bucketed by the given granularity.
//
// Implementation uses generate_series + LEFT JOIN to ensure zero-count
// buckets appear in the result. Without this, gaps in traffic break charts.
//
// date_trunc truncates each event's occurred_at to the bucket boundary,
// then we LEFT JOIN against the complete series of expected buckets.
func (r *AnalyticsQueryRepository) GetTimeSeries(
	ctx context.Context,
	shortCode string,
	start, end time.Time,
	granularity analytics.Granularity,
) (*analytics.TimeSeries, error) {
	ctx, span := otel.Tracer(analyticsQueryTracerName).Start(ctx, "AnalyticsQuery.GetTimeSeries",
		attribute.String("url.short_code", shortCode),
		attribute.String("analytics.granularity", string(granularity)),
	)
	defer span.End()

	// Map granularity to PostgreSQL interval string and date_trunc field.
	truncField, interval, err := granularityToSQL(granularity)
	if err != nil {
		return nil, err
	}

	// This query:
	//   1. Generates a complete series of time buckets (generate_series)
	//   2. LEFT JOINs actual event data against the series
	//   3. Returns 0 for buckets with no events (COALESCE)
	//
	// Using a CTE for clarity — PostgreSQL optimises it efficiently.
	query := fmt.Sprintf(`
		WITH buckets AS (
			SELECT generate_series(
				date_trunc('%s', $2::TIMESTAMPTZ),
				date_trunc('%s', $3::TIMESTAMPTZ - INTERVAL '1 microsecond'),
				INTERVAL '%s'
			) AS bucket_start
		),
		event_counts AS (
			SELECT
				date_trunc('%s', occurred_at) AS bucket_start,
				COUNT(*) AS clicks,
				COUNT(DISTINCT ip_hash) AS unique_ips
			FROM redirect_events
			WHERE short_code   = $1
			  AND occurred_at >= $2
			  AND occurred_at <  $3
			  AND is_bot       = false
			GROUP BY date_trunc('%s', occurred_at)
		)
		SELECT
			b.bucket_start,
			COALESCE(e.clicks, 0)      AS clicks,
			COALESCE(e.unique_ips, 0)  AS unique_ips
		FROM buckets b
		LEFT JOIN event_counts e USING (bucket_start)
		ORDER BY b.bucket_start ASC`,
		truncField, truncField, interval,
		truncField, truncField,
	)

	rows, err := r.db.Replica().Query(ctx, query, shortCode, start, end)
	if err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("analytics: timeseries query: %w", err)
	}
	defer rows.Close()

	var points []analytics.TimeSeriesPoint
	for rows.Next() {
		var p analytics.TimeSeriesPoint
		if err := rows.Scan(&p.BucketStart, &p.Clicks, &p.UniqueIPs); err != nil {
			return nil, fmt.Errorf("analytics: scanning timeseries row: %w", err)
		}
		points = append(points, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("analytics: timeseries row iteration: %w", err)
	}

	span.SetAttributes(attribute.Int("analytics.point_count", len(points)))

	return &analytics.TimeSeries{
		ShortCode:   shortCode,
		Granularity: granularity,
		WindowStart: start,
		WindowEnd:   end,
		Points:      points,
	}, nil
}

// GetBreakdown returns click counts grouped by a single dimension.
//
// Security: dimension is validated by the domain layer before reaching here.
// The ColumnName() method returns a hardcoded string (not user input),
// eliminating the SQL injection risk from dynamic column names.
func (r *AnalyticsQueryRepository) GetBreakdown(
	ctx context.Context,
	shortCode string,
	start, end time.Time,
	dim analytics.Dimension,
) (*analytics.Breakdown, error) {
	ctx, span := otel.Tracer(analyticsQueryTracerName).Start(ctx, "AnalyticsQuery.GetBreakdown",
		attribute.String("url.short_code", shortCode),
		attribute.String("analytics.dimension", string(dim)),
	)
	defer span.End()

	// ColumnName() is a compile-time safe string — not user input.
	colName := dim.ColumnName()
	if colName == "" {
		return nil, fmt.Errorf("analytics: unknown dimension %q", dim)
	}

	// COALESCE maps NULL dimension values to the empty string.
	// NULL arises for unclassified traffic (unknown country, unknown device).
	// We return them as "" rather than filtering them out — the caller decides
	// whether to display "Unknown" in the UI.
	query := fmt.Sprintf(`
		SELECT
			COALESCE(%s, '') AS dimension_value,
			COUNT(*)          AS clicks
		FROM redirect_events
		WHERE short_code   = $1
		  AND occurred_at >= $2
		  AND occurred_at <  $3
		  AND is_bot       = false
		GROUP BY %s
		ORDER BY clicks DESC
		LIMIT 50`, colName, colName)

	rows, err := r.db.Replica().Query(ctx, query, shortCode, start, end)
	if err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("analytics: breakdown query: %w", err)
	}
	defer rows.Close()

	var counts []analytics.DimensionCount
	var totalClicks int64
	for rows.Next() {
		var dc analytics.DimensionCount
		if err := rows.Scan(&dc.Value, &dc.Clicks); err != nil {
			return nil, fmt.Errorf("analytics: scanning breakdown row: %w", err)
		}
		totalClicks += dc.Clicks
		counts = append(counts, dc)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("analytics: breakdown row iteration: %w", err)
	}

	// Compute percentages now that we have the total.
	if totalClicks > 0 {
		for i := range counts {
			counts[i].Percentage = float64(counts[i].Clicks) / float64(totalClicks) * 100
		}
	}

	return &analytics.Breakdown{
		ShortCode:   shortCode,
		Dimension:   dim,
		WindowStart: start,
		WindowEnd:   end,
		TotalClicks: totalClicks,
		Counts:      counts,
	}, nil
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// granularityToSQL maps a Granularity to its PostgreSQL date_trunc field name
// and generate_series interval string.
func granularityToSQL(g analytics.Granularity) (truncField, interval string, err error) {
	switch g {
	case analytics.Granularity1Minute:
		return "minute", "1 minute", nil
	case analytics.Granularity1Hour:
		return "hour", "1 hour", nil
	case analytics.Granularity1Day:
		return "day", "1 day", nil
	default:
		return "", "", fmt.Errorf("analytics: unsupported granularity %q", g)
	}
}
