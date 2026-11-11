-- Monotonic cursor for resumable Server-Sent Events.
--
-- Clients reconnect with Last-Event-ID and the API replays ticket_events with a
-- greater seq. A single sequence for the whole table (rather than per org) is
-- simpler and still strictly ordered per org; the org/seq index is what the
-- replay query uses.

CREATE SEQUENCE IF NOT EXISTS ticket_events_seq;

ALTER TABLE ticket_events ADD COLUMN IF NOT EXISTS seq bigint;

-- Backfill existing rows in the same order the outbox dispatcher uses. The
-- row_number is only used for the one-time backfill; after that the sequence
-- assigns cursor values.
WITH ordered AS (
  SELECT id, row_number() OVER (ORDER BY created_at, id) AS rn
  FROM ticket_events
  WHERE seq IS NULL
)
UPDATE ticket_events AS te
SET seq = ordered.rn
FROM ordered
WHERE te.id = ordered.id;

-- Next nextval returns max(seq) + 1, or 1 when the table is empty.
SELECT setval(
  'ticket_events_seq',
  COALESCE((SELECT max(seq) FROM ticket_events), 0) + 1,
  false
);

ALTER TABLE ticket_events ALTER COLUMN seq SET DEFAULT nextval('ticket_events_seq');
ALTER TABLE ticket_events ALTER COLUMN seq SET NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_ticket_events_seq ON ticket_events (seq);
CREATE INDEX IF NOT EXISTS idx_ticket_events_org_seq ON ticket_events (org_id, seq);
