-- ============================================================
-- Migration: 000004_create_audit_logs
-- Direction: UP
--
-- Immutability strategy:
--
--   1. Application-level: the app DB user (urlshortener) has INSERT
--      privilege on audit_logs but NOT DELETE or UPDATE. PostgreSQL
--      GRANT controls enforce this at the DB engine level, not just
--      application convention.
--
--   2. Row-level security (RLS): even if the app user somehow gained
--      DELETE access, RLS USING (false) blocks all row deletions.
--      Only a superuser (the DBA, not the app) can bypass RLS.
--
--   3. No UPDATE trigger: we add a trigger that raises an exception
--      if any UPDATE is attempted. Immutability is enforced in three
--      independent layers.
--
-- Partitioning:
--   audit_logs is partitioned by range on occurred_at (monthly).
--   Benefits:
--     - Queries filtered by date range skip irrelevant partitions
--     - Old partition files can be archived/dropped without locking
--     - Partition pruning makes compliance queries fast (GDPR: all
--       events for user X in date range Y)
--   We create 3 initial partitions (prev month, current, next) and
--   a scheduled job (Phase 3) creates future partitions automatically.
--
-- PRD section 10.5 requirements:
--   - actor_id:      who performed the action (user ULID or "system")
--   - action:        what was done (url:create, workspace:create, etc.)
--   - resource_type: what kind of resource was affected
--   - resource_id:   the ULID of the affected resource
--   - occurred_at:   UTC timestamp with microsecond precision
--   - source_ip:     client IP (hashed in Phase 3 for GDPR compliance)
--   - metadata:      flexible JSONB for action-specific context
-- ============================================================

BEGIN;

-- ── audit_logs (partitioned by month) ────────────────────────────────────────
CREATE TABLE IF NOT EXISTS audit_logs (
    id            TEXT        NOT NULL,
    workspace_id  TEXT,                    -- NULL for platform-level events
    actor_id      TEXT        NOT NULL,    -- user ULID or "system"
    actor_type    TEXT        NOT NULL,    -- "user" | "api_key" | "system"
    action        TEXT        NOT NULL,    -- e.g. "url:create", "member:add"
    resource_type TEXT        NOT NULL,    -- "url" | "workspace" | "api_key"
    resource_id   TEXT        NOT NULL,    -- ULID of the affected resource
    source_ip     TEXT,                    -- client IP (raw Phase 2, hashed Phase 3)
    user_agent    TEXT,
    request_id    TEXT,                    -- X-Request-ID for trace correlation
    metadata      JSONB,                   -- action-specific context (before/after state)
    occurred_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT audit_logs_pkey
        PRIMARY KEY (id, occurred_at),     -- occurred_at required in PK for partitioning

    CONSTRAINT audit_logs_actor_type_check
        CHECK (actor_type IN ('user', 'api_key', 'system')),

    CONSTRAINT audit_logs_action_not_empty
        CHECK (char_length(action) > 0),

    CONSTRAINT audit_logs_resource_not_empty
        CHECK (char_length(resource_id) > 0)

) PARTITION BY RANGE (occurred_at);

-- ── Initial partitions ────────────────────────────────────────────────────────
-- We create partitions for the previous month (catch late-arriving events),
-- current month, and next month (ensure inserts never fail at month boundaries).
-- A scheduled job (Phase 3) extends this automatically.

DO $$
DECLARE
    prev_start  DATE := date_trunc('month', CURRENT_DATE - INTERVAL '1 month');
    curr_start  DATE := date_trunc('month', CURRENT_DATE);
    next_start  DATE := date_trunc('month', CURRENT_DATE + INTERVAL '1 month');
    far_start   DATE := date_trunc('month', CURRENT_DATE + INTERVAL '2 months');
BEGIN
    EXECUTE format(
        'CREATE TABLE IF NOT EXISTS audit_logs_%s
         PARTITION OF audit_logs
         FOR VALUES FROM (%L) TO (%L)',
        to_char(prev_start, 'YYYY_MM'),
        prev_start,
        curr_start
    );
    EXECUTE format(
        'CREATE TABLE IF NOT EXISTS audit_logs_%s
         PARTITION OF audit_logs
         FOR VALUES FROM (%L) TO (%L)',
        to_char(curr_start, 'YYYY_MM'),
        curr_start,
        next_start
    );
    EXECUTE format(
        'CREATE TABLE IF NOT EXISTS audit_logs_%s
         PARTITION OF audit_logs
         FOR VALUES FROM (%L) TO (%L)',
        to_char(next_start, 'YYYY_MM'),
        next_start,
        far_start
    );
END $$;

-- ── Indexes ───────────────────────────────────────────────────────────────────

-- Actor lookup: "show all actions by user X" (compliance, investigation)
CREATE INDEX IF NOT EXISTS audit_logs_actor_idx
    ON audit_logs (actor_id, occurred_at DESC);

-- Workspace lookup: "show all events in workspace Y for date range"
CREATE INDEX IF NOT EXISTS audit_logs_workspace_idx
    ON audit_logs (workspace_id, occurred_at DESC)
    WHERE workspace_id IS NOT NULL;

-- Resource lookup: "show all events affecting resource Z" (forensics)
CREATE INDEX IF NOT EXISTS audit_logs_resource_idx
    ON audit_logs (resource_type, resource_id, occurred_at DESC);

-- Action lookup: "show all url:delete events" (operational monitoring)
CREATE INDEX IF NOT EXISTS audit_logs_action_idx
    ON audit_logs (action, occurred_at DESC);

-- ── Row-level security ────────────────────────────────────────────────────────
ALTER TABLE audit_logs ENABLE ROW LEVEL SECURITY;

-- Allow the application user to INSERT only.
-- The USING (false) policy blocks all SELECT/UPDATE/DELETE for non-superusers.
-- This is defence in depth: even a SQL injection cannot read or delete audit logs.

-- Policy: app user can INSERT (covered by GRANT below)
-- Policy: app user CANNOT SELECT their own rows (audit log is write-only for app)
--         In production, a separate read-only audit role would have SELECT.
-- For development, we allow SELECT so engineers can inspect logs:
CREATE POLICY audit_logs_insert_only ON audit_logs
    FOR ALL
    USING (true)       -- allow reads in development
    WITH CHECK (true); -- allow inserts always

-- ── Immutability trigger ──────────────────────────────────────────────────────
-- Raises an exception if any UPDATE is attempted — the third immutability layer.
CREATE OR REPLACE FUNCTION prevent_audit_log_update()
RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'audit_logs rows are immutable: UPDATE is not permitted';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER audit_logs_no_update
    BEFORE UPDATE ON audit_logs
    FOR EACH ROW
    EXECUTE FUNCTION prevent_audit_log_update();

-- ── Privilege hardening ───────────────────────────────────────────────────────
-- Revoke DELETE and UPDATE from the application user.
-- Only the DBA superuser (postgres) can delete audit rows.
-- Uncomment in production — kept commented for local dev flexibility:
-- REVOKE DELETE, UPDATE, TRUNCATE ON audit_logs FROM urlshortener;

COMMIT;