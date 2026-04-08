-- ============================================================
-- Migration: 000003_create_api_keys
-- Direction: UP
--
-- Security design:
--
-- API key storage model (PRD section 10.1):
--   The raw key is shown to the user ONCE at creation and never stored.
--   We store: bcrypt( sha256(raw_key) )
--
--   Why double-hash (SHA-256 then bcrypt)?
--   - bcrypt has a 72-byte input limit. Long keys are silently truncated.
--   - SHA-256 collapses any key length to exactly 32 bytes (64 hex chars)
--     before bcrypt, eliminating the truncation risk.
--   - bcrypt's adaptive cost factor (cost=12) makes brute-force attacks
--     against a leaked hash table computationally infeasible.
--
-- key_prefix:
--   We store the first 8 chars of the raw key (e.g. "urlsk_ab")
--   in plaintext for identification only — not for authentication.
--   This lets users identify which key to revoke without exposing
--   the full secret. Never use key_prefix for auth decisions.
--
-- scope:
--   Stored as TEXT array. Values: 'read', 'write', 'admin'.
--   PostgreSQL text array is the right type — it's queried with @>
--   operator and has native GIN index support for future scope queries.
--
-- last_used_at:
--   Updated asynchronously on every successful auth — never blocks
--   the request. Used for "show when key was last active" in the UI.
-- ============================================================

BEGIN;

CREATE TABLE IF NOT EXISTS api_keys (
    id             TEXT        NOT NULL,
    workspace_id   TEXT        NOT NULL,
    name           TEXT        NOT NULL,           -- Human label (e.g. "CI/CD Pipeline")
    key_hash       TEXT        NOT NULL,           -- bcrypt(sha256(raw_key))
    key_prefix     TEXT        NOT NULL,           -- First 8 chars of raw key (display only)
    scopes         TEXT[]      NOT NULL DEFAULT '{}',
    created_by     TEXT        NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at     TIMESTAMPTZ,                    -- NULL = no expiry
    revoked_at     TIMESTAMPTZ,                    -- NULL = active
    last_used_at   TIMESTAMPTZ,                    -- NULL = never used

    CONSTRAINT api_keys_pkey
        PRIMARY KEY (id),

    CONSTRAINT api_keys_workspace_fk
        FOREIGN KEY (workspace_id) REFERENCES workspaces (id) ON DELETE CASCADE,

    CONSTRAINT api_keys_name_length
        CHECK (char_length(name) BETWEEN 1 AND 100),

    -- key_prefix format: "urlsk_" + 8 alphanumeric chars
    CONSTRAINT api_keys_prefix_format
        CHECK (key_prefix ~ '^urlsk_[a-zA-Z0-9]{8}$'),

    CONSTRAINT api_keys_scopes_valid
        CHECK (scopes <@ ARRAY['read','write','admin']::TEXT[])
);

-- ── Indexes ───────────────────────────────────────────────────────────────────

-- Primary auth lookup: find a key candidate by its prefix.
-- We cannot index key_hash directly for auth (bcrypt is not searchable).
-- Instead, the auth flow is:
--   1. Client sends: "urlsk_ab1cde2f:<rest-of-key>"
--   2. We extract the prefix "urlsk_ab1cde2f" (first 14 chars)
--   3. SELECT all non-revoked, non-expired keys in the workspace
--      WHERE key_prefix = $prefix  (this index)
--   4. bcrypt.CompareHashAndPassword(stored_hash, sha256(full_key))
-- The prefix narrows candidates to usually 1 row before the expensive bcrypt.
CREATE INDEX IF NOT EXISTS api_keys_prefix_idx
    ON api_keys (key_prefix)
    WHERE revoked_at IS NULL;

-- Workspace listing: GET /api/v1/api-keys returns keys for one workspace.
CREATE INDEX IF NOT EXISTS api_keys_workspace_id_idx
    ON api_keys (workspace_id, created_at DESC)
    WHERE revoked_at IS NULL;

COMMIT;