DROP INDEX IF EXISTS idx_email_deliveries_event_recipient_template;
ALTER TABLE email_deliveries DROP COLUMN IF EXISTS event_id;

DROP INDEX IF EXISTS idx_tickets_solved_resolved;
DROP INDEX IF EXISTS idx_tickets_resolution_due_breach;
DROP INDEX IF EXISTS idx_tickets_first_response_due_breach;
ALTER TABLE tickets DROP COLUMN IF EXISTS resolution_breached_at;
ALTER TABLE tickets DROP COLUMN IF EXISTS first_response_breached_at;

DROP INDEX IF EXISTS idx_ticket_events_undispatched;
ALTER TABLE ticket_events DROP COLUMN IF EXISTS dispatched_at;
