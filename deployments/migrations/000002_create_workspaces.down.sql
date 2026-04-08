-- ============================================================
-- Migration: 000002_create_workspaces
-- Direction: DOWN
-- ============================================================

BEGIN;

-- Remove FK before dropping the referenced table.
ALTER TABLE urls DROP CONSTRAINT IF EXISTS urls_workspace_fk;

DROP TRIGGER IF EXISTS workspaces_set_updated_at ON workspaces;

-- CASCADE drops workspace_members automatically via the FK constraint.
DROP TABLE IF EXISTS workspace_members;
DROP TABLE IF EXISTS workspaces;

COMMIT;