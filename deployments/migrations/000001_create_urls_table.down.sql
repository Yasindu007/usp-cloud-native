-- =============================================================
-- Migration: 000001_create_urls_table
-- Direction: DOWN
-- Description: Rolls back the urls table creation.
--
-- Down migrations must be idempotent (IF EXISTS) because a failed
-- partial rollback may have already dropped some objects.
-- =============================================================

BEGIN;

DROP TRIGGER IF EXISTS urls_set_updated_at ON urls;
DROP FUNCTION IF EXISTS set_updated_at();
DROP TABLE IF EXISTS urls;

COMMIT;