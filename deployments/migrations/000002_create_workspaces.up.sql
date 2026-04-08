-- ============================================================
-- Migration: 000002_create_workspaces
-- Direction: UP
--
-- Design decisions:
--
-- workspaces table:
--   slug is a human-friendly, URL-safe unique identifier.
--   Example: org "Acme Corp" → slug "acme-corp".
--   Used in the API path and for branded short domains (Phase 3).
--   Regex enforced at DB level: lowercase alphanumeric + hyphens only.
--
-- workspace_members table:
--   role uses TEXT + CHECK (not ENUM) for the same reason as urls.status —
--   easier to add new roles without table locks.
--   A user can only have one role per workspace (PRIMARY KEY on the pair).
--   The owner role is set when the workspace is created and never via
--   the member invite flow — enforced at the application layer.
--
-- urls.workspace_id FK:
--   We add a FK from urls.workspace_id → workspaces.id here, not in
--   migration 000001, because workspaces did not exist yet.
--   DEFERRABLE INITIALLY DEFERRED allows inserting both in the same txn.
-- ============================================================

BEGIN;

-- ── workspaces ────────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS workspaces (
    id         TEXT        NOT NULL,
    name       TEXT        NOT NULL,
    slug       TEXT        NOT NULL,
    plan_tier  TEXT        NOT NULL DEFAULT 'free',
    owner_id   TEXT        NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT workspaces_pkey
        PRIMARY KEY (id),

    CONSTRAINT workspaces_name_unique
        UNIQUE (name),

    CONSTRAINT workspaces_slug_unique
        UNIQUE (slug),

    -- Slug: lowercase letters, digits, hyphens. No leading/trailing hyphens.
    CONSTRAINT workspaces_slug_format
        CHECK (slug ~ '^[a-z0-9][a-z0-9\-]*[a-z0-9]$' OR char_length(slug) = 1),

    CONSTRAINT workspaces_plan_tier_check
        CHECK (plan_tier IN ('free', 'pro', 'enterprise')),

    CONSTRAINT workspaces_name_length
        CHECK (char_length(name) BETWEEN 1 AND 100),

    CONSTRAINT workspaces_slug_length
        CHECK (char_length(slug) BETWEEN 1 AND 63)
);

-- ── workspace_members ─────────────────────────────────────────────────────────
-- Junction table between users (identified by user_id from JWT sub claim)
-- and workspaces. One row = one membership with a single role.
CREATE TABLE IF NOT EXISTS workspace_members (
    workspace_id TEXT        NOT NULL,
    user_id      TEXT        NOT NULL,
    role         TEXT        NOT NULL,
    invited_by   TEXT,                  -- NULL for the workspace owner (self)
    joined_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT workspace_members_pkey
        PRIMARY KEY (workspace_id, user_id),

    CONSTRAINT workspace_members_workspace_fk
        FOREIGN KEY (workspace_id) REFERENCES workspaces (id) ON DELETE CASCADE,

    CONSTRAINT workspace_members_role_check
        CHECK (role IN ('owner', 'admin', 'editor', 'viewer'))
);

-- ── Indexes ───────────────────────────────────────────────────────────────────

-- Lookup: "list all workspaces this user belongs to"
-- Used by GET /api/v1/workspaces
CREATE INDEX IF NOT EXISTS workspace_members_user_id_idx
    ON workspace_members (user_id);

-- Lookup: "list all members of this workspace"
-- Used by GET /api/v1/workspaces/{id}/members
CREATE INDEX IF NOT EXISTS workspace_members_workspace_id_idx
    ON workspace_members (workspace_id);

-- Slug lookup for human-readable URLs
CREATE INDEX IF NOT EXISTS workspaces_slug_idx
    ON workspaces (slug);

-- ── Seed: default workspace for Phase 1 data ─────────────────────────────────
-- Any URLs created during Phase 1 (before workspaces existed) used
-- workspace IDs with no corresponding row in workspaces. We create:
--   1. a stable default workspace for local/dev flows
--   2. placeholder legacy workspaces for any distinct urls.workspace_id
-- This ensures the FK added below succeeds on pre-workspace data.
INSERT INTO workspaces (id, name, slug, plan_tier, owner_id)
VALUES ('ws_default', 'Default Workspace', 'default', 'free', 'usr_default')
ON CONFLICT (id) DO NOTHING;

INSERT INTO workspaces (id, name, slug, plan_tier, owner_id)
SELECT DISTINCT
    u.workspace_id,
    'Legacy Workspace ' || substr(md5(u.workspace_id), 1, 8),
    'legacy-' || substr(md5(u.workspace_id), 1, 16),
    'free',
    'usr_default'
FROM urls u
WHERE NOT EXISTS (
    SELECT 1
    FROM workspaces w
    WHERE w.id = u.workspace_id
);

INSERT INTO workspace_members (workspace_id, user_id, role)
SELECT w.id, 'usr_default', 'owner'
FROM workspaces w
WHERE w.owner_id = 'usr_default'
ON CONFLICT DO NOTHING;

-- ── Add FK from urls → workspaces ─────────────────────────────────────────────
-- This was deferred from migration 000001 because workspaces didn't exist yet.
-- Seed rows must exist before the constraint is added because existing urls
-- may already reference legacy workspace IDs.
ALTER TABLE urls
    ADD CONSTRAINT urls_workspace_fk
    FOREIGN KEY (workspace_id) REFERENCES workspaces (id)
    DEFERRABLE INITIALLY DEFERRED;

-- ── Triggers ──────────────────────────────────────────────────────────────────
-- Reuse the set_updated_at() function created in migration 000001.
CREATE TRIGGER workspaces_set_updated_at
    BEFORE UPDATE ON workspaces
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

COMMIT;
