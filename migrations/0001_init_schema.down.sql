-- 0001_init_schema.down.sql
-- Reverse of 0001_init_schema.up.sql
-- Drop order = reverse creation order; children before parents.

BEGIN;

-- ---------------------------------------------------------------
-- INTEGRATIONS (reverse of creation)
-- ---------------------------------------------------------------
DROP TABLE IF EXISTS idempotency_keys;
DROP TABLE IF EXISTS webhook_deliveries;
DROP TABLE IF EXISTS webhook_subscriptions;

-- ---------------------------------------------------------------
-- NOTIFICATIONS
-- ---------------------------------------------------------------
DROP TABLE IF EXISTS email_deliveries;
DROP TABLE IF EXISTS notification_preferences;
DROP TABLE IF EXISTS notifications;

-- ---------------------------------------------------------------
-- TICKET CORE (reverse of creation)
-- ---------------------------------------------------------------
DROP TABLE IF EXISTS macros;
DROP TABLE IF EXISTS satisfaction_ratings;
DROP TABLE IF EXISTS ticket_events;
DROP TABLE IF EXISTS ticket_watchers;
DROP TABLE IF EXISTS ticket_tags;
DROP TABLE IF EXISTS tags;
DROP TABLE IF EXISTS attachments;
DROP TABLE IF EXISTS comments;
DROP TABLE IF EXISTS tickets;

-- ---------------------------------------------------------------
-- SLA
-- ---------------------------------------------------------------
DROP TABLE IF EXISTS sla_policies;

-- ---------------------------------------------------------------
-- AUTH
-- ---------------------------------------------------------------
DROP TABLE IF EXISTS verification_tokens;
DROP TABLE IF EXISTS api_keys;
DROP TABLE IF EXISTS refresh_tokens;

-- ---------------------------------------------------------------
-- TENANCY AND IDENTITY (reverse of creation)
-- ---------------------------------------------------------------
DROP TABLE IF EXISTS invitations;
DROP TABLE IF EXISTS team_members;
DROP TABLE IF EXISTS teams;
DROP TABLE IF EXISTS customers;
DROP TABLE IF EXISTS memberships;
DROP TABLE IF EXISTS users;
DROP TABLE IF EXISTS organizations;

-- ---------------------------------------------------------------
-- ENUMS (only after every table using them is gone)
-- ---------------------------------------------------------------
DROP TYPE IF EXISTS notification_channel;
DROP TYPE IF EXISTS delivery_status;
DROP TYPE IF EXISTS verification_type;
DROP TYPE IF EXISTS actor_type;
DROP TYPE IF EXISTS comment_author_type;
DROP TYPE IF EXISTS ticket_channel;
DROP TYPE IF EXISTS ticket_priority;
DROP TYPE IF EXISTS ticket_status;
DROP TYPE IF EXISTS org_role;

COMMIT;
