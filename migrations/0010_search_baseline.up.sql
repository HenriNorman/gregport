-- Postgres full-text search baseline. This is derived data only: tickets and
-- comments remain the source of truth, and the vectors are generated columns
-- so they can never drift from the row that produced them.
--
-- The baseline is the fallback when Elasticsearch is unavailable and the
-- reference implementation used to compare latency on a seeded dataset.

ALTER TABLE tickets
  ADD COLUMN search_vector tsvector
  GENERATED ALWAYS AS (to_tsvector('english', coalesce(subject, ''))) STORED;

-- GIN indexes make `search_vector @@ websearch_to_tsquery(...)` an index scan
-- instead of a sequential scan over every ticket/comment in the database.
CREATE INDEX idx_tickets_search_vector
  ON tickets USING GIN (search_vector);

ALTER TABLE comments
  ADD COLUMN search_vector tsvector
  GENERATED ALWAYS AS (to_tsvector('english', coalesce(body, ''))) STORED;

CREATE INDEX idx_comments_search_vector
  ON comments USING GIN (search_vector);
