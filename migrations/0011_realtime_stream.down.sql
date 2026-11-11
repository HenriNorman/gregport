DROP INDEX IF EXISTS idx_ticket_events_org_seq;
DROP INDEX IF EXISTS idx_ticket_events_seq;
ALTER TABLE ticket_events ALTER COLUMN seq DROP DEFAULT;
ALTER TABLE ticket_events DROP COLUMN IF EXISTS seq;
DROP SEQUENCE IF EXISTS ticket_events_seq;
