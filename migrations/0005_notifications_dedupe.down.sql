-- Reverse of 0005_notifications_dedupe.up.sql

DROP INDEX IF EXISTS idx_notifications_event_user_dedupe;
DROP INDEX IF EXISTS idx_webhook_deliveries_retry;

ALTER TABLE notifications DROP COLUMN IF EXISTS event_id;
