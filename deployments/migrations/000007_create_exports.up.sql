BEGIN;

CREATE TABLE IF NOT EXISTS exports (
    id                  TEXT        NOT NULL PRIMARY KEY,
    workspace_id        TEXT        NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    requested_by        TEXT        NOT NULL,
    format              TEXT        NOT NULL,
    status              TEXT        NOT NULL DEFAULT 'pending',
    date_from           TIMESTAMPTZ NOT NULL,
    date_to             TIMESTAMPTZ NOT NULL,
    include_bots        BOOLEAN     NOT NULL DEFAULT FALSE,
    file_path           TEXT,
    row_count           BIGINT,
    file_size_bytes     BIGINT,
    error_message       TEXT,
    download_token      TEXT,
    download_expires_at TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    worker_started_at   TIMESTAMPTZ,
    completed_at        TIMESTAMPTZ,

    CONSTRAINT exports_format_check
        CHECK (format IN ('csv', 'json_lines')),
    CONSTRAINT exports_status_check
        CHECK (status IN ('pending', 'processing', 'completed', 'failed')),
    CONSTRAINT exports_date_range_check
        CHECK (date_to > date_from),
    CONSTRAINT exports_max_window_check
        CHECK (date_to - date_from <= INTERVAL '365 days')
);

CREATE INDEX IF NOT EXISTS exports_pending_idx
    ON exports (created_at ASC)
    WHERE status = 'pending';

CREATE INDEX IF NOT EXISTS exports_workspace_idx
    ON exports (workspace_id, created_at DESC);

CREATE INDEX IF NOT EXISTS exports_expired_idx
    ON exports (download_expires_at)
    WHERE status = 'completed' AND download_expires_at IS NOT NULL;

COMMIT;
