-- Multi-tenant support ticketing SaaS: PostgreSQL schema
-- Import into dbdiagram.io: Import > From SQL > PostgreSQL
-- Conventions: uuid PKs, timestamptz, org_id on every tenant-owned table,
-- soft delete (deleted_at) only on tables that need it.

-- ---------------------------------------------------------------
-- ENUMS
-- ---------------------------------------------------------------
CREATE TYPE org_role AS ENUM ('owner', 'admin', 'agent');
CREATE TYPE ticket_status AS ENUM ('new', 'open', 'pending', 'solved', 'closed');
CREATE TYPE ticket_priority AS ENUM ('low', 'normal', 'high', 'urgent');
CREATE TYPE ticket_channel AS ENUM ('api', 'email', 'widget', 'web');
CREATE TYPE comment_author_type AS ENUM ('user', 'customer', 'system');
CREATE TYPE actor_type AS ENUM ('user', 'customer', 'api_key', 'system');
CREATE TYPE verification_type AS ENUM ('email_verification', 'password_reset');
CREATE TYPE delivery_status AS ENUM ('pending', 'sent', 'failed', 'dead');
CREATE TYPE notification_channel AS ENUM ('in_app', 'email');

-- ---------------------------------------------------------------
-- TENANCY AND IDENTITY
-- ---------------------------------------------------------------
CREATE TABLE organizations (
  id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name           text NOT NULL,
  slug           text NOT NULL UNIQUE,
  plan           text NOT NULL DEFAULT 'free',
  settings       jsonb NOT NULL DEFAULT '{}',
  ticket_counter bigint NOT NULL DEFAULT 0,  -- per-org ticket numbers (#1042)
  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now(),
  deleted_at     timestamptz
);

CREATE TABLE users (
  id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  email             text NOT NULL UNIQUE,
  password_hash     text NOT NULL,
  name              text NOT NULL,
  email_verified_at timestamptz,
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now(),
  deleted_at        timestamptz
);

CREATE TABLE memberships (
  id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id     uuid NOT NULL REFERENCES organizations(id),
  user_id    uuid NOT NULL REFERENCES users(id),
  role       org_role NOT NULL DEFAULT 'agent',
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (org_id, user_id)
);

CREATE TABLE customers (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id      uuid NOT NULL REFERENCES organizations(id),
  email       text NOT NULL,
  name        text,
  external_id text,  -- the business's own id for this customer (e.g. rider id)
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now(),
  deleted_at  timestamptz,
  UNIQUE (org_id, email)
);

CREATE TABLE teams (
  id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id     uuid NOT NULL REFERENCES organizations(id),
  name       text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (org_id, name)
);

CREATE TABLE team_members (
  team_id          uuid NOT NULL REFERENCES teams(id),
  user_id          uuid NOT NULL REFERENCES users(id),
  last_assigned_at timestamptz,  -- round-robin auto-assign
  PRIMARY KEY (team_id, user_id)
);

CREATE TABLE invitations (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id      uuid NOT NULL REFERENCES organizations(id),
  email       text NOT NULL,
  role        org_role NOT NULL DEFAULT 'agent',
  token_hash  text NOT NULL UNIQUE,
  invited_by  uuid NOT NULL REFERENCES users(id),
  expires_at  timestamptz NOT NULL,
  accepted_at timestamptz,
  created_at  timestamptz NOT NULL DEFAULT now()
);

-- ---------------------------------------------------------------
-- AUTH
-- ---------------------------------------------------------------
CREATE TABLE refresh_tokens (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id     uuid NOT NULL REFERENCES users(id),
  org_id      uuid REFERENCES organizations(id),  -- active org when issued
  family_id   uuid NOT NULL,                      -- rotation chain, reuse = revoke family
  token_hash  text NOT NULL UNIQUE,
  user_agent  text,
  ip          text,
  expires_at  timestamptz NOT NULL,
  revoked_at  timestamptz,
  created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE api_keys (
  id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id             uuid NOT NULL REFERENCES organizations(id),
  name               text NOT NULL,
  prefix             text NOT NULL,          -- visible part, e.g. tk_live_ab12
  key_hash           text NOT NULL UNIQUE,
  created_by         uuid NOT NULL REFERENCES users(id),
  rate_limit_per_min integer NOT NULL DEFAULT 600,
  last_used_at       timestamptz,
  expires_at         timestamptz,
  revoked_at         timestamptz,
  created_at         timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE verification_tokens (
  id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id    uuid NOT NULL REFERENCES users(id),
  type       verification_type NOT NULL,
  token_hash text NOT NULL UNIQUE,
  expires_at timestamptz NOT NULL,
  used_at    timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);

-- ---------------------------------------------------------------
-- SLA
-- ---------------------------------------------------------------
CREATE TABLE sla_policies (
  id                     uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id                 uuid NOT NULL REFERENCES organizations(id),
  name                   text NOT NULL,
  priority               ticket_priority NOT NULL,
  first_response_minutes integer NOT NULL,
  resolution_minutes     integer NOT NULL,
  created_at             timestamptz NOT NULL DEFAULT now(),
  UNIQUE (org_id, priority)
);

-- ---------------------------------------------------------------
-- TICKET CORE
-- ---------------------------------------------------------------
CREATE TABLE tickets (
  id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id                uuid NOT NULL REFERENCES organizations(id),
  number                bigint NOT NULL,  -- per-org sequential number
  requester_id          uuid NOT NULL REFERENCES customers(id),
  assignee_id           uuid REFERENCES users(id),
  team_id               uuid REFERENCES teams(id),
  sla_policy_id         uuid REFERENCES sla_policies(id),
  merged_into_id        uuid REFERENCES tickets(id),
  subject               text NOT NULL,
  status                ticket_status NOT NULL DEFAULT 'new',
  priority              ticket_priority NOT NULL DEFAULT 'normal',
  channel               ticket_channel NOT NULL DEFAULT 'api',
  first_response_due_at timestamptz,
  resolution_due_at     timestamptz,
  first_response_at     timestamptz,
  resolved_at           timestamptz,
  closed_at             timestamptz,
  created_at            timestamptz NOT NULL DEFAULT now(),
  updated_at            timestamptz NOT NULL DEFAULT now(),
  deleted_at            timestamptz,
  UNIQUE (org_id, number)
);

CREATE TABLE comments (
  id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id             uuid NOT NULL REFERENCES organizations(id),
  ticket_id          uuid NOT NULL REFERENCES tickets(id),
  author_type        comment_author_type NOT NULL,
  author_user_id     uuid REFERENCES users(id),      -- set when author_type = 'user'
  author_customer_id uuid REFERENCES customers(id),  -- set when author_type = 'customer'
  body               text NOT NULL,
  is_internal        boolean NOT NULL DEFAULT false, -- internal note, hidden from customer
  created_at         timestamptz NOT NULL DEFAULT now(),
  deleted_at         timestamptz
);

CREATE TABLE attachments (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id      uuid NOT NULL REFERENCES organizations(id),
  ticket_id   uuid NOT NULL REFERENCES tickets(id),
  comment_id  uuid REFERENCES comments(id),
  storage_key text NOT NULL,  -- object key in S3/MinIO
  filename    text NOT NULL,
  mime_type   text NOT NULL,
  size_bytes  bigint NOT NULL,
  created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE tags (
  id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id     uuid NOT NULL REFERENCES organizations(id),
  name       text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (org_id, name)
);

CREATE TABLE ticket_tags (
  ticket_id uuid NOT NULL REFERENCES tickets(id),
  tag_id    uuid NOT NULL REFERENCES tags(id),
  PRIMARY KEY (ticket_id, tag_id)
);

CREATE TABLE ticket_watchers (
  ticket_id uuid NOT NULL REFERENCES tickets(id),
  user_id   uuid NOT NULL REFERENCES users(id),
  PRIMARY KEY (ticket_id, user_id)
);

-- Event backbone: status/assignee history, audit trail, source for
-- notifications, webhooks, search indexing and SLA reports.
CREATE TABLE ticket_events (
  id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id     uuid NOT NULL REFERENCES organizations(id),
  ticket_id  uuid NOT NULL REFERENCES tickets(id),
  actor_type actor_type NOT NULL,
  actor_id   uuid,            -- polymorphic, no FK (user, customer or api_key id)
  event_type text NOT NULL,   -- ticket.created, ticket.assigned, comment.added, sla.breached...
  data       jsonb NOT NULL DEFAULT '{}',
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE satisfaction_ratings (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id      uuid NOT NULL REFERENCES organizations(id),
  ticket_id   uuid NOT NULL UNIQUE REFERENCES tickets(id),
  customer_id uuid NOT NULL REFERENCES customers(id),
  score       smallint NOT NULL,  -- 1 to 5, enforce in app or with a CHECK later
  comment     text,
  created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE macros (
  id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id     uuid NOT NULL REFERENCES organizations(id),
  name       text NOT NULL,
  body       text NOT NULL,
  actions    jsonb NOT NULL DEFAULT '{}',  -- e.g. set status, add tag
  created_by uuid NOT NULL REFERENCES users(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (org_id, name)
);

-- ---------------------------------------------------------------
-- NOTIFICATIONS
-- ---------------------------------------------------------------
CREATE TABLE notifications (
  id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id     uuid NOT NULL REFERENCES organizations(id),
  user_id    uuid NOT NULL REFERENCES users(id),
  type       text NOT NULL,  -- ticket_assigned, new_comment, sla_breach, mention
  ticket_id  uuid REFERENCES tickets(id),
  comment_id uuid REFERENCES comments(id),
  payload    jsonb NOT NULL DEFAULT '{}',
  read_at    timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE notification_preferences (
  user_id    uuid NOT NULL REFERENCES users(id),
  event_type text NOT NULL,
  channel    notification_channel NOT NULL,
  enabled    boolean NOT NULL DEFAULT true,
  PRIMARY KEY (user_id, event_type, channel)
);

CREATE TABLE email_deliveries (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id        uuid NOT NULL REFERENCES organizations(id),
  to_email      text NOT NULL,
  template      text NOT NULL,
  payload       jsonb NOT NULL DEFAULT '{}',
  status        delivery_status NOT NULL DEFAULT 'pending',
  attempts      integer NOT NULL DEFAULT 0,
  last_error    text,
  next_retry_at timestamptz,
  sent_at       timestamptz,
  created_at    timestamptz NOT NULL DEFAULT now()
);

-- ---------------------------------------------------------------
-- INTEGRATIONS
-- ---------------------------------------------------------------
CREATE TABLE webhook_subscriptions (
  id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id     uuid NOT NULL REFERENCES organizations(id),
  url        text NOT NULL,
  secret     text NOT NULL,                  -- used to HMAC-sign payloads
  events     jsonb NOT NULL DEFAULT '[]',    -- subscribed event types
  is_active  boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE webhook_deliveries (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id          uuid NOT NULL REFERENCES organizations(id),
  subscription_id uuid NOT NULL REFERENCES webhook_subscriptions(id),
  event_id        uuid REFERENCES ticket_events(id),
  event_type      text NOT NULL,
  payload         jsonb NOT NULL,
  status          delivery_status NOT NULL DEFAULT 'pending',
  attempts        integer NOT NULL DEFAULT 0,
  response_status integer,
  last_error      text,
  next_retry_at   timestamptz,
  delivered_at    timestamptz,
  created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE idempotency_keys (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id          uuid NOT NULL REFERENCES organizations(id),
  key             text NOT NULL,
  request_hash    text NOT NULL,
  response_status integer,
  response_body   jsonb,
  created_at      timestamptz NOT NULL DEFAULT now(),
  expires_at      timestamptz NOT NULL,
  UNIQUE (org_id, key)
);

-- ---------------------------------------------------------------
-- INDEXES (tenant-first composite indexes)
-- ---------------------------------------------------------------
CREATE INDEX idx_tickets_org_status_created ON tickets (org_id, status, created_at);
CREATE INDEX idx_tickets_org_assignee_status ON tickets (org_id, assignee_id, status);
CREATE INDEX idx_tickets_org_requester ON tickets (org_id, requester_id);
CREATE INDEX idx_tickets_org_sla_due ON tickets (org_id, resolution_due_at);
CREATE INDEX idx_comments_ticket_created ON comments (ticket_id, created_at);
CREATE INDEX idx_ticket_events_ticket_created ON ticket_events (ticket_id, created_at);
CREATE INDEX idx_notifications_user_unread ON notifications (user_id, read_at, created_at);
CREATE INDEX idx_refresh_tokens_user ON refresh_tokens (user_id);
CREATE INDEX idx_refresh_tokens_family ON refresh_tokens (family_id);
CREATE INDEX idx_webhook_deliveries_retry ON webhook_deliveries (status, next_retry_at);
CREATE INDEX idx_email_deliveries_retry ON email_deliveries (status, next_retry_at);
CREATE INDEX idx_attachments_ticket ON attachments (ticket_id);
