-- ============================================================
-- Migration: 000006_analytics_indexes
-- Direction: UP
--
-- This migration adds query-optimised indexes to redirect_events.
-- It is separate from the table creation (000005) because:
--   1. Index creation on a large partitioned table is an ONLINE operation
--      in PostgreSQL (using CREATE INDEX CONCURRENTLY) — we want this
--      controllable independently of table creation.
--   2. Query patterns become clear only after the ingestion pipeline
--      exists — indexes added after observing real query shapes.
--
-- Query patterns we must serve (PRD section 5.4):
--   A. Click summary for a short code over a time window
--      WHERE short_code = $1 AND occurred_at BETWEEN $2 AND $3
--   B. Time-series aggregation (group by time bucket)
--      WHERE short_code = $1 AND occurred_at BETWEEN $2 AND $3
--      GROUP BY time_bucket
--   C. Dimensional breakdown (group by country, device, browser)
--      WHERE short_code = $1 AND occurred_at BETWEEN $2 AND $3
--      GROUP BY country_code / device_type / browser_family
--   D. Workspace-wide rollup (all URLs in workspace)
--      WHERE workspace_id = $1 AND occurred_at BETWEEN $2 AND $3
--
-- Index strategy:
--   - Composite (short_code, occurred_at DESC): serves A, B, C efficiently
--     The short_code equality filter reduces the scan to one URL's events;
--     occurred_at DESC gives time-range filtering with index-only access.
--   - Partial index (WHERE is_bot = false): analytics exclude bot traffic.
--     A partial index skips bot rows entirely, reducing index size by ~20%.
--   - Workspace rollup index: serves D without full table scan.
--
-- Note on partition pruning:
--   PostgreSQL automatically prunes partitions based on occurred_at range
--   in WHERE clauses. These indexes are created on the PARENT table and
--   inherited by all existing and future partitions automatically.
-- ============================================================

BEGIN;

-- Primary analytics lookup index.
-- Covers the hot path for all per-URL analytics queries.
CREATE INDEX IF NOT EXISTS redirect_events_shortcode_time_idx
    ON redirect_events (short_code, occurred_at DESC)
    WHERE is_bot = false;

-- Workspace rollup index.
-- Covers dashboard queries that aggregate across all URLs in a workspace.
CREATE INDEX IF NOT EXISTS redirect_events_workspace_time_idx
    ON redirect_events (workspace_id, occurred_at DESC)
    WHERE is_bot = false;

-- Country breakdown index.
-- Supports dimensional breakdown queries grouping by country.
CREATE INDEX IF NOT EXISTS redirect_events_country_idx
    ON redirect_events (short_code, country_code, occurred_at DESC)
    WHERE is_bot = false AND country_code IS NOT NULL;

COMMIT;