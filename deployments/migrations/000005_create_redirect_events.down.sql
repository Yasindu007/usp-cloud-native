BEGIN;

DROP TRIGGER IF EXISTS redirect_events_no_update ON redirect_events;
DROP FUNCTION IF EXISTS prevent_redirect_event_update();
DROP TABLE IF EXISTS redirect_events CASCADE;

COMMIT;
