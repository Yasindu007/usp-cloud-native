BEGIN;
DROP INDEX IF EXISTS redirect_events_shortcode_time_idx;
DROP INDEX IF EXISTS redirect_events_workspace_time_idx;
DROP INDEX IF EXISTS redirect_events_country_idx;
COMMIT;