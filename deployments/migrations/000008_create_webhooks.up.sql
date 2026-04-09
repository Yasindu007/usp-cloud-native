BEGIN;

CREATE TABLE IF NOT EXISTS webhooks (
    id              TEXT        NOT NULL PRIMARY KEY,
    workspace_id    TEXT        NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    name            TEXT        NOT NULL,
    url             TEXT        NOT NULL,
    secret          TEXT        NOT NULL,
    events          TEXT[]      NOT NULL DEFAULT '{}',
    status          TEXT        NOT NULL DEFAULT 'active',
    created_by      TEXT        NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_success_at TIMESTAMPTZ,
    last_failure_at TIMESTAMPTZ,
    failure_count   INT         NOT NULL DEFAULT 0,
    CONSTRAINT webhooks_status_check CHECK (status IN ('active', 'failing', 'disabled')),
    CONSTRAINT webhooks_name_length CHECK (char_length(name) BETWEEN 1 AND 100),
    CONSTRAINT webhooks_url_not_empty CHECK (char_length(url) > 0 AND char_length(url) <= 2048),
    CONSTRAINT webhooks_events_valid CHECK (
        events <@ ARRAY['url.created','url.updated','url.deleted','redirect.received']::TEXT[]
    ),
    CONSTRAINT webhooks_events_not_empty CHECK (cardinality(events) > 0)
);

CREATE TABLE IF NOT EXISTS webhook_deliveries (
    id               TEXT        NOT NULL PRIMARY KEY,
    webhook_id       TEXT        NOT NULL REFERENCES webhooks (id) ON DELETE CASCADE,
    workspace_id     TEXT        NOT NULL,
    event_type       TEXT        NOT NULL,
    event_id         TEXT        NOT NULL,
    payload          JSONB       NOT NULL,
    status           TEXT        NOT NULL DEFAULT 'pending',
    attempt_count    INT         NOT NULL DEFAULT 0,
    next_attempt_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_attempt_at  TIMESTAMPTZ,
    last_http_status INT,
    last_error       TEXT,
    delivered_at     TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT webhook_deliveries_status_check CHECK (
        status IN ('pending', 'delivering', 'delivered', 'failed', 'abandoned')
    ),
    CONSTRAINT webhook_deliveries_event_type_check CHECK (
        event_type IN ('url.created','url.updated','url.deleted','redirect.received')
    )
);

CREATE INDEX IF NOT EXISTS webhooks_workspace_idx
    ON webhooks (workspace_id, created_at DESC)
    WHERE status != 'disabled';

CREATE INDEX IF NOT EXISTS webhook_deliveries_worker_idx
    ON webhook_deliveries (next_attempt_at, status)
    WHERE status = 'pending';

CREATE INDEX IF NOT EXISTS webhook_deliveries_reclaim_idx
    ON webhook_deliveries (last_attempt_at)
    WHERE status = 'delivering';

CREATE INDEX IF NOT EXISTS webhook_deliveries_webhook_idx
    ON webhook_deliveries (webhook_id, created_at DESC);

CREATE TRIGGER webhooks_set_updated_at
    BEFORE UPDATE ON webhooks
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

COMMIT;
