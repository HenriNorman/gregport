DROP INDEX IF EXISTS idx_comments_search_vector;
ALTER TABLE comments DROP COLUMN IF EXISTS search_vector;

DROP INDEX IF EXISTS idx_tickets_search_vector;
ALTER TABLE tickets DROP COLUMN IF EXISTS search_vector;
