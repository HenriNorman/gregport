ALTER TABLE webhook_deliveries
  DROP CONSTRAINT webhook_deliveries_subscription_id_fkey,
  ADD CONSTRAINT webhook_deliveries_subscription_id_fkey
    FOREIGN KEY (subscription_id)
    REFERENCES webhook_subscriptions(id);

DROP INDEX IF EXISTS idx_webhook_deliveries_subscription_created;
DROP INDEX IF EXISTS idx_webhook_deliveries_event_subscription;

ALTER TABLE webhook_deliveries
  DROP COLUMN IF EXISTS duration_ms,
  DROP COLUMN IF EXISTS response_body;

ALTER TABLE webhook_subscriptions
  DROP COLUMN IF EXISTS disabled_at,
  DROP COLUMN IF EXISTS last_failure_at,
  DROP COLUMN IF EXISTS consecutive_failures;
