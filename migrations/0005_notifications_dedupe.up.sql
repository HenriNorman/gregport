-- Notifications dedupe key and webhook delivery semantics.

-- Dedup key so a replayed notify job creates at most one notification per
-- (event, user). NULL event_id rows are exempt (legacy/system notifications).
ALTER TABLE notifications ADD COLUMN event_id uuid REFERENCES ticket_events(id);

CREATE UNIQUE INDEX idx_notifications_event_user_dedupe
  ON notifications (event_id, user_id)
  WHERE event_id IS NOT NULL;

-- Webhook delivery retry index mirrors email_deliveries for polling pending
-- deliveries by next retry time.
CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_retry
  ON webhook_deliveries (status, next_retry_at);
