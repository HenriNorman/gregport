-- Webhook delivery hardening: persistent failure state, response capture and
-- idempotent delivery rows.

ALTER TABLE webhook_subscriptions
  ADD COLUMN consecutive_failures integer NOT NULL DEFAULT 0,
  ADD COLUMN last_failure_at timestamptz,
  ADD COLUMN disabled_at timestamptz;

ALTER TABLE webhook_deliveries
  ADD COLUMN response_body text,
  ADD COLUMN duration_ms integer;

-- One delivery per (subscription, event) makes replaying the outbox converge
-- on the existing row instead of sending the event twice.
CREATE UNIQUE INDEX idx_webhook_deliveries_event_subscription
  ON webhook_deliveries (subscription_id, event_id)
  WHERE event_id IS NOT NULL;

-- Delivery-log pagination is newest-first per subscription.
CREATE INDEX idx_webhook_deliveries_subscription_created
  ON webhook_deliveries (subscription_id, created_at DESC, id);

-- Deleting a subscription should remove its delivery history rather than
-- failing on the foreign key.
ALTER TABLE webhook_deliveries
  DROP CONSTRAINT webhook_deliveries_subscription_id_fkey,
  ADD CONSTRAINT webhook_deliveries_subscription_id_fkey
    FOREIGN KEY (subscription_id)
    REFERENCES webhook_subscriptions(id)
    ON DELETE CASCADE;
