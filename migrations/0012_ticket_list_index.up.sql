-- The unfiltered ticket list is the hottest tenant read: filter by org, exclude
-- soft-deleted rows, order by (created_at DESC, id DESC), limit 25.
-- EXPLAIN showed a parallel sequential scan plus a top-N sort (about 208 ms on
-- the 700k-ticket tenant). This partial composite index matches the filter and
-- sort exactly and stays smaller because deleted rows are excluded.
CREATE INDEX idx_tickets_org_created_active
    ON tickets (org_id, created_at DESC, id DESC)
    WHERE deleted_at IS NULL;
