BEGIN;

DROP TRIGGER IF EXISTS audit_logs_no_update ON audit_logs;
DROP FUNCTION IF EXISTS prevent_audit_log_update();

-- Drop partitions first (CASCADE drops them when parent is dropped)
DROP TABLE IF EXISTS audit_logs CASCADE;

COMMIT;