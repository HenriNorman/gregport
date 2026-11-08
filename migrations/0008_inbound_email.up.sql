-- Inbound email: record every received message, dedupe on Message-ID, and
-- route replies to tickets or create new tickets from email.
CREATE TABLE inbound_emails (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id      uuid NOT NULL REFERENCES organizations(id),
  message_id  text NOT NULL UNIQUE,
  in_reply_to text,
  from_email  text NOT NULL,
  from_name   text,
  recipient   text NOT NULL,
  subject     text NOT NULL,
  body        text NOT NULL,
  status      text NOT NULL DEFAULT 'received',
  ticket_id   uuid REFERENCES tickets(id),
  comment_id  uuid REFERENCES comments(id),
  raw         text NOT NULL,
  received_at timestamptz NOT NULL DEFAULT now()
);

-- Look up an org's recent inbound mail by receipt time.
CREATE INDEX idx_inbound_emails_org_received
  ON inbound_emails (org_id, received_at DESC);

-- Route an In-Reply-To back to the message it references.
CREATE INDEX idx_inbound_emails_message_id
  ON inbound_emails (message_id);
