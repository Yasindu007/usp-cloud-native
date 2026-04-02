-- =============================================================
-- Migration: 000001_create_urls_table
-- Direction: UP
-- Description: Creates the core urls table and supporting indexes.
--
-- Design notes:
--   - id uses TEXT to store ULID strings (26 chars, lexicographically
--     sortable, URL-safe). We do NOT use uuid type because ULID gives
--     us natural time-ordering for cursor-based pagination at zero cost.
--   - status uses TEXT with a CHECK constraint rather than a Postgres
--     ENUM. ENUMs require ALTER TYPE to add values, which can lock the
--     table. TEXT + CHECK is easier to migrate and equally safe.
--   - click_count is a denormalized counter for fast reads. It is
--     incremented atomically via UPDATE ... SET click_count = click_count + 1.
--     The authoritative count is the analytics.redirect_events table (Phase 3).
--   - expires_at is nullable: NULL means no expiry (indefinite).
--   - deleted_at is nullable: NULL means not deleted (soft-delete pattern).
--   - All timestamps are TIMESTAMPTZ (timezone-aware UTC) — never TIMESTAMP.
--     Storing timezone-naive timestamps is a common source of DST bugs.
-- =============================================================

BEGIN;

-- urls is the primary aggregate table for the URL shortening domain.
-- Every short code in the system has exactly one row here.
CREATE TABLE IF NOT EXISTS urls (
    id              TEXT        NOT NULL,
    workspace_id    TEXT        NOT NULL,
    short_code      TEXT        NOT NULL,
    original_url    TEXT        NOT NULL,
    title           TEXT,
    status          TEXT        NOT NULL DEFAULT 'active',
    expires_at      TIMESTAMPTZ,
    created_by      TEXT        NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at      TIMESTAMPTZ,
    click_count     BIGINT      NOT NULL DEFAULT 0,

    CONSTRAINT urls_pkey
        PRIMARY KEY (id),

    -- short_code must be globally unique across all workspaces.
    -- This is the lookup key for every redirect request.
    CONSTRAINT urls_short_code_unique
        UNIQUE (short_code),

    -- Enforce valid status values without ENUM lock risk.
    CONSTRAINT urls_status_check
        CHECK (status IN ('active', 'expired', 'disabled', 'deleted')),

    -- Prevent empty short codes at the DB layer (defense in depth).
    CONSTRAINT urls_short_code_not_empty
        CHECK (char_length(short_code) >= 3),

    -- Prevent empty original URLs at the DB layer.
    CONSTRAINT urls_original_url_not_empty
        CHECK (char_length(original_url) > 0),

    -- Enforce max URL length from PRD section 9.1 (8192 chars).
    CONSTRAINT urls_original_url_max_length
        CHECK (char_length(original_url) <= 8192),

    -- click_count must never go negative.
    CONSTRAINT urls_click_count_non_negative
        CHECK (click_count >= 0)
);

-- ── Indexes ────────────────────────────────────────────────────────────────

-- Primary lookup index: every redirect resolution does a lookup by short_code.
-- This is already covered by the UNIQUE constraint above (creates a B-tree index).
-- Listed here for documentation clarity.
-- INDEX: urls_short_code_unique (created automatically by UNIQUE constraint)

-- Workspace listing index: GET /api/v1/urls filters and sorts by workspace_id + id.
-- Composite index on (workspace_id, id) supports cursor-based pagination.
-- id is included because ULIDs are time-ordered — ORDER BY id gives chronological order.
CREATE INDEX IF NOT EXISTS urls_workspace_id_id_idx
    ON urls (workspace_id, id);

-- Status filter index: listing URLs filtered by status within a workspace.
-- Used by GET /api/v1/urls?status=active
CREATE INDEX IF NOT EXISTS urls_workspace_status_idx
    ON urls (workspace_id, status)
    WHERE deleted_at IS NULL;

-- Expiration index: used by the expiration worker (Phase 3) to find
-- URLs where expires_at has passed and status is still 'active'.
-- Partial index (WHERE expires_at IS NOT NULL) skips the large majority
-- of rows that have no expiry, making the worker query very fast.
CREATE INDEX IF NOT EXISTS urls_expiry_idx
    ON urls (expires_at)
    WHERE expires_at IS NOT NULL AND status = 'active';

-- Creator index: supports filtering by created_by within a workspace.
CREATE INDEX IF NOT EXISTS urls_created_by_idx
    ON urls (workspace_id, created_by);

-- ── Trigger: auto-update updated_at ────────────────────────────────────────
-- Rather than relying on the application to set updated_at, we use a
-- trigger. This ensures updated_at is always accurate even for manual
-- DBA updates and batch operations.

CREATE OR REPLACE FUNCTION set_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER urls_set_updated_at
    BEFORE UPDATE ON urls
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

COMMIT;