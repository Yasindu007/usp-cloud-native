BEGIN;
DROP TRIGGER IF EXISTS webhooks_set_updated_at ON webhooks;
DROP TABLE IF EXISTS webhook_deliveries;
DROP TABLE IF EXISTS webhooks;
COMMIT;
