-- ============================================================
-- Migration: 000005_create_redirect_events
-- Direction: UP
--
-- Design decisions:
--
-- Partitioning strategy (range on occurred_at, monthly):
--   redirect_events is the highest-volume table in the platform.
--   At 50 redirects/URL/day across 10k active URLs → 500k events/day
--   → ~15M rows/month → ~180M rows/year.
--   Without partitioning, a single table scan for analytics queries
--   covering 6 months would touch 90M rows. With monthly partitions,
--   the planner skips irrelevant partition files entirely (partition pruning).
--   Monthly granularity balances:
--     - Query performance (partition pruning for date-range queries)
--     - Management overhead (1 new partition/month to create)
--     - Archival (old partitions can be moved to cold storage or DROPped)
--
-- ip_hash (not raw IP):
--   PRD section 14.1 (GDPR): "IP addresses captured in redirect events
--   MUST be hashed immediately on ingestion using SHA-256 with a daily
--   rotating salt."
--   The raw IP is NEVER stored. ip_hash allows counting unique visitors
--   per day (same hash = same IP on same day) without storing PII.
--   Changing the salt daily means a user's hash changes each day —
--   cross-day tracking via IP is not possible.
--
-- Append-only enforcement:
--   Like audit_logs, redirect_events must never be updated.
--   We add a trigger to reject any UPDATE attempt.
--   DELETE is permitted only for partition management (archival/purge).
--
-- short_code (not FK to urls.id):
--   We store short_code directly, not a FK to urls.id.
--   Reasons:
--   1. FK on a partitioned table has restrictions and overhead in PG
--   2. If a URL is soft-deleted, its historical events must remain
--   3. Analytics queries join on short_code, not on the ULID
--   4. short_code is the natural lookup key for the redirect service
-- ============================================================

BEGIN;

CREATE TABLE IF NOT EXISTS redirect_events (
    id            TEXT        NOT NULL,
    short_code    TEXT        NOT NULL,
    workspace_id  TEXT        NOT NULL,
    occurred_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    -- Identity (privacy-preserving)
    ip_hash       TEXT,            -- SHA-256(ip + daily_salt), NULL for bots
    user_agent    TEXT,

    -- Parsed UA dimensions
    device_type   TEXT,            -- "mobile" | "desktop" | "tablet" | "bot" | "unknown"
    browser_family TEXT,           -- "Chrome" | "Firefox" | "Safari" | "bot" | "unknown"
    os_family     TEXT,            -- "Windows" | "macOS" | "iOS" | "Android" | "unknown"
    is_bot        BOOLEAN NOT NULL DEFAULT FALSE,

    -- Geographic (Phase 4: real MaxMind lookup; Phase 3: stub returns "XX")
    country_code  CHAR(2),         -- ISO 3166-1 alpha-2 or "XX" for unknown

    -- Attribution
    referrer_domain TEXT,          -- Extracted from Referer header domain only
    referrer_raw    TEXT,          -- Full Referer header (truncated to 1024 chars)

    -- Correlation
    request_id    TEXT,

    CONSTRAINT redirect_events_pkey
        PRIMARY KEY (id, occurred_at),   -- occurred_at required in PK for partitioning

    CONSTRAINT redirect_events_short_code_not_empty
        CHECK (char_length(short_code) >= 3),

    CONSTRAINT redirect_events_device_type_check
        CHECK (device_type IN ('mobile','desktop','tablet','bot','unknown') OR device_type IS NULL)

) PARTITION BY RANGE (occurred_at);

-- ── Initial partitions (same pattern as audit_logs) ───────────────────────────
DO $$
DECLARE
    prev_start  DATE := date_trunc('month', CURRENT_DATE - INTERVAL '1 month');
    curr_start  DATE := date_trunc('month', CURRENT_DATE);
    next_start  DATE := date_trunc('month', CURRENT_DATE + INTERVAL '1 month');
    far_start   DATE := date_trunc('month', CURRENT_DATE + INTERVAL '2 months');
BEGIN
    EXECUTE format(
        'CREATE TABLE IF NOT EXISTS redirect_events_%s
         PARTITION OF redirect_events
         FOR VALUES FROM (%L) TO (%L)',
        to_char(prev_start, 'YYYY_MM'), prev_start, curr_start
    );
    EXECUTE format(
        'CREATE TABLE IF NOT EXISTS redirect_events_%s
         PARTITION OF redirect_events
         FOR VALUES FROM (%L) TO (%L)',
        to_char(curr_start, 'YYYY_MM'), curr_start, next_start
    );
    EXECUTE format(
        'CREATE TABLE IF NOT EXISTS redirect_events_%s
         PARTITION OF redirect_events
         FOR VALUES FROM (%L) TO (%L)',
        to_char(next_start, 'YYYY_MM'), next_start, far_start
    );
END $$;

-- ── Indexes (created on the parent, inherited by all partitions) ──────────────

-- Primary analytics query: click count + time series for a short code
-- "SELECT COUNT(*), date_trunc('hour', occurred_at) FROM redirect_events
--  WHERE short_code = $1 AND occurred_at BETWEEN $2 AND $3 GROUP BY 2"
CREATE INDEX IF NOT EXISTS redirect_events_short_code_time_idx
    ON redirect_events (short_code, occurred_at DESC);

-- Workspace-level analytics: all events for a workspace in a date range
CREATE INDEX IF NOT EXISTS redirect_events_workspace_time_idx
    ON redirect_events (workspace_id, occurred_at DESC);

-- Bot filtering: aggregate human-only traffic
-- WHERE is_bot = FALSE is used in most analytics queries
CREATE INDEX IF NOT EXISTS redirect_events_human_idx
    ON redirect_events (short_code, occurred_at DESC)
    WHERE is_bot = FALSE;

-- Country breakdown queries
CREATE INDEX IF NOT EXISTS redirect_events_country_idx
    ON redirect_events (short_code, country_code, occurred_at DESC)
    WHERE is_bot = FALSE AND country_code IS NOT NULL;

-- ── Immutability trigger ──────────────────────────────────────────────────────
CREATE OR REPLACE FUNCTION prevent_redirect_event_update()
RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'redirect_events rows are immutable: UPDATE is not permitted';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER redirect_events_no_update
    BEFORE UPDATE ON redirect_events
    FOR EACH ROW
    EXECUTE FUNCTION prevent_redirect_event_update();

COMMIT;