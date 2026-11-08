-- Satisfaction ratings submitted from the public rating link.

CREATE TABLE ticket_ratings (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id      uuid NOT NULL REFERENCES organizations(id),
  ticket_id   uuid NOT NULL REFERENCES tickets(id),
  customer_id uuid NOT NULL REFERENCES customers(id),
  score       smallint NOT NULL CHECK (score BETWEEN 1 AND 5),
  comment     text,
  created_at  timestamptz NOT NULL DEFAULT now(),
  UNIQUE (ticket_id)
);

CREATE INDEX idx_ticket_ratings_org_created
  ON ticket_ratings (org_id, created_at DESC);
