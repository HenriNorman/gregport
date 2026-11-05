-- Worker, outbox dispatch, SLA breach tracking and email idempotency.

-- Outbox: events stay undispatched until the dispatcher has enqueued every
-- downstream job. The partial index keeps the polling query cheap.
ALTER TABLE ticket_events ADD COLUMN dispatched_at timestamptz;

CREATE INDEX idx_ticket_events_undispatched
  ON ticket_events (created_at, id)
  WHERE dispatched_at IS NULL;

-- SLA breach markers ensure a ticket produces at most one breach event per
-- deadline, even when several workers look at it concurrently.
ALTER TABLE tickets ADD COLUMN first_response_breached_at timestamptz;
ALTER TABLE tickets ADD COLUMN resolution_breached_at     timestamptz;

CREATE INDEX idx_tickets_first_response_due_breach
  ON tickets (first_response_due_at)
  WHERE first_response_due_at IS NOT NULL
    AND first_response_breached_at IS NULL
    AND deleted_at IS NULL;

CREATE INDEX idx_tickets_resolution_due_breach
  ON tickets (resolution_due_at)
  WHERE resolution_due_at IS NOT NULL
    AND resolution_breached_at IS NULL
    AND deleted_at IS NULL;

-- Auto-close scans solved tickets by the moment they were resolved.
CREATE INDEX idx_tickets_solved_resolved
  ON tickets (resolved_at)
  WHERE status = 'solved' AND deleted_at IS NULL;

-- Email delivery idempotency: one row per (event, recipient, template). A
-- duplicate job reuses the existing row instead of sending twice.
ALTER TABLE email_deliveries ADD COLUMN event_id uuid REFERENCES ticket_events(id);

CREATE UNIQUE INDEX idx_email_deliveries_event_recipient_template
  ON email_deliveries (event_id, to_email, template)
  WHERE event_id IS NOT NULL;
