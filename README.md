# Gregport

[![CI](https://github.com/HenriNorman/gregport/actions/workflows/ci.yml/badge.svg)](https://github.com/HenriNorman/gregport/actions/workflows/ci.yml)

A multi-tenant support ticketing SaaS built with Go and PostgreSQL. It supports organizations, users, memberships, tickets, comments, SLA policies, notifications, and webhooks.

---

## Project Status

| Area | Status |
|:--|:--|
| Authentication | Complete — signup, login, refresh, logout, `/v1/me` |
| JWT and refresh tokens | Complete and tested — refresh rotation, hashing, and reuse detection |
| Tickets | Complete — CRUD, comments, state machine, keyset pagination, optimistic concurrency |
| Teams | Complete — CRUD and membership management |
| Ticket assignment | Complete — manual (assignee or team) and round-robin (`auto: true`) |
| Tags | Complete — create, list, attach, detach (idempotent) |
| Watchers | Complete — add/remove (idempotent); assignee and agent commenters auto-watch |
| Timeline | Complete — cursor-paginated `ticket_events`, oldest first |
| Merge | Complete — source closed and pointed at target; merged tickets read-only |
| Macros | Complete — CRUD (admin/owner) and apply to a ticket (any member) |
| Invitations | Complete — create (admin/owner, grant table), list pending, revoke, accept (new and existing user) |
| Members | Complete — list (any member), role change and removal (admin/owner), last-owner protection |
| Organization settings | Complete — read (any member) and partial update (admin/owner), timezone/priority/support-email validation |
| SLA policies | Complete — CRUD per org, one policy per priority, reference-safe deletion |
| Dashboard | Complete — org-scoped ticket counts by status and priority, plus average satisfaction rating |
| Caching | Complete — Redis cache for org settings, SLA policies, and dashboard counts with write invalidation |
| Satisfaction ratings | Complete — signed expiring links in solved email, unique per ticket, tight public rate limit, dashboard average |
| Idempotency | Complete — tenant-scoped PostgreSQL key store and HTTP middleware (pending, replay, conflict, expiry) |
| API keys | Complete — create, list, revoke; `gp_live_` keys authenticate as agent-level principals; `last_used_at` throttled in Redis |
| Audit log | Complete — every state-changing action in feat 5 writes an `audit_logs` row in the same transaction |
| Structured logging | Complete — JSON to stdout; one line per request with request ID, route pattern, status, duration and principal IDs |
| Error envelope | Complete — one `{"error":{...}}` shape with `code`, `message`, `details` and `request_id` |
| Production basics | Complete — logging, error envelope, timeouts, health checks, graceful shutdown, config validation, multi-stage image, compose stack, CI and twelve-factor summary |
| Background worker | Complete — `cmd/worker` with config, logs, connections, health probes, graceful shutdown, separate I/O and CPU asynq servers, the outbox dispatcher, queue statistics and the Asynqmon dead-letter view |
| Email notifications | Complete — ticket, comment, assignment and SLA-breach events fan out through the outbox and asynq; deliveries are idempotent, circuit-broken and sent over SMTP |
| In-app notifications | Complete — outbox fan-out to assignee and watchers, dedupe per event/user, preference matrix, unread listing with cursor pagination, idempotent mark-read |
| SLA automation | Complete — due dates are applied on create/priority change, breaches are recorded by a scheduler, and solved tickets auto-close |
| Housekeeping | Complete — scheduled pruning of expired idempotency keys, dispatched events, audit logs and delivered email rows |
| Webhooks | Complete — tenant-scoped subscriptions, HMAC-signed delivery, SSRF-hardened HTTP client, retry schedule, delivery log with cursor pagination and redelivery, auto-disable with admin notifications |
| Inbound email | Complete — provider-signed inbound webhook, Message-ID dedupe, `[#1042]` and `In-Reply-To` routing to comment or new ticket, raw email recorded |

### Authentication

The authentication implementation includes:

- JWT access tokens.
- Hashed refresh tokens.
- Refresh-token rotation.
- Refresh-token reuse detection.
- Token-family revocation.
- Login, refresh, and logout services.

Every authenticated request carries a verified `user_id`, `org_id`, and `role` in its request context.

### Tickets

The ticket domain is implemented and tested.

- **Create:** Single transaction covering the counter, customer, ticket, comment, and event.
- **List:** Keyset pagination.
- **Get and update:** Optimistic concurrency using `If-Match` and the ticket's `version`.
- **Delete:** Soft deletion, restricted to admins and owners.
- **Comments:** Implemented and exposed.
- **State transitions:** Table-driven state machine.

Tenant isolation is tested across ticket endpoints. Requests for tickets belonging to another organization return `404`, preventing the API from revealing their existence.

The ticket domain's test suite covers CRUD, concurrency, tenant isolation, and every sub-resource (tags, watchers, timeline, merge, macros). Run `go test ./internal/tickets/ -v` for the current count.

### Teams and Assignment

Teams support creation, listing, updating, and deletion, along with adding and removing members (admin/owner only).

Ticket assignment supports manual assignment to an individual agent or a team, and round-robin (`auto: true`) within a team.

Round-robin picks the team member with the oldest `last_assigned_at` inside the same transaction, with `SKIP LOCKED` so concurrent assigns do not pick the same agent.

### Organization Settings

`GET /v1/organization/settings` is available to every authenticated member.
`PATCH /v1/organization/settings` requires `owner` or `admin`.

Settings live in the `organizations.settings` JSONB column and are exposed as a
typed document:

```json
{"settings":{"timezone":"UTC","default_priority":"normal","support_email":""},"updated_at":"2026-11-04T10:00:00Z"}
```

- Updates are partial: omitted fields keep their stored value.
- Missing values read back as `UTC` and `normal`.
- The timezone must be a valid IANA name; the IANA database is embedded in the
  binary so validation also works in the minimal Alpine image.
- The support email must be a bare address and is lowercased. Send an empty
  string to clear it.
- Settings reads and writes are always scoped to the caller's org; another
  tenant's settings are never reachable.

### SLA Policies

The SLA policy endpoints expose the `sla_policies` table:

| Method | Path | Access |
|:--|:--|:--|
| `GET` | `/v1/sla-policies` | any member |
| `GET` | `/v1/sla-policies/{id}` | any member |
| `POST` | `/v1/sla-policies` | owner or admin |
| `PATCH` | `/v1/sla-policies/{id}` | owner or admin |
| `DELETE` | `/v1/sla-policies/{id}` | owner or admin |

- An org may have at most one policy per priority; a duplicate returns `409`
  with code `sla_policy_exists`.
- Names are trimmed and limited to 100 characters.
- First-response and resolution minutes must be positive, at most one year,
  and resolution cannot be shorter than first response.
- Deletion is blocked with `409 sla_policy_in_use` while a ticket still
  references the policy, so due-date history is never orphaned.
- Every read and write is scoped to the caller's org; another tenant's policy
  returns `404`.
- The ticket service applies the org's cached policy on create: the matching
  priority supplies `sla_policy_id`, `first_response_due_at` and
  `resolution_due_at`. Changing a ticket's priority recomputes both deadlines
  from the ticket's creation time; a priority with no policy clears them.
- `internal/sla/engine.go` is a pure calculator. `WallClock` adds calendar
  minutes behind the `Calculator` interface, so a business-hours calendar can
  replace it later without touching the ticket service. Non-positive targets
  return `ErrInvalidDeadline`; a lookup or calculation failure logs a warning
  and leaves the ticket without SLA dates rather than failing the write.

### Dashboard Counts

`GET /v1/dashboard/counts` returns a single org-scoped summary:

```json
{
  "total": 5,
  "open": 3,
  "unassigned": 2,
  "overdue": 1,
  "by_status": {"new": 1, "open": 1, "pending": 1, "solved": 1, "closed": 1},
  "by_priority": {"low": 2, "normal": 1, "high": 1, "urgent": 1},
  "average_rating": 4.6,
  "rating_count": 18
}
```

- `open` is `new + open + pending`.
- `unassigned` counts open tickets with no assignee.
- `overdue` counts unresolved tickets whose `resolution_due_at` has passed.
- `average_rating` is the mean satisfaction score, and `rating_count` the
  number of ratings; both are `0` when the org has no ratings.
- Soft-deleted tickets are excluded from every ticket number.
- Counts and the rating aggregate are computed with one query scoped to the
  caller's org.

### Notifications

In-app notifications are scoped to the authenticated user and the current org;
there is no way to read or mark another user's notifications.

| Method | Path | Purpose |
|:--|:--|:--|
| `GET` | `/v1/notifications` | List newest first; `unread=true` filters unread rows; `limit` and opaque `cursor` paginate |
| `POST` | `/v1/notifications/{id}/read` | Mark one notification read (idempotent, 204) |
| `POST` | `/v1/notifications/read-all` | Mark every unread notification read and return `{"updated": n}` |
| `GET` | `/v1/notification-preferences` | Return the full event-type × channel matrix |
| `PUT` | `/v1/notification-preferences` | Replace preferences; omitted entries revert to enabled |

The list response mirrors the timeline cursor style:

```json
{
  "notifications": [
    {
      "id": "550e8400-e29b-41d4-a716-446655440000",
      "type": "new_comment",
      "ticket_id": "6ba7b810-9dad-11d1-80b4-00c04fd430c8",
      "comment_id": "6ba7b811-9dad-11d1-80b4-00c04fd430c8",
      "payload": {"ticket_number": 1042, "ticket_subject": "Cannot log in"},
      "created_at": "2026-01-15T10:00:00Z"
    }
  ],
  "next_cursor": "eyJ0IjoiMjAyNi0wMS0xNVQxMDowMDowMFoiLCJpIjoiNTUwZTg0MDAtIn0"
}
```

Preferences are default-on, so clients only need to send the entries they want
to change. A `PUT` replaces the stored set and always returns the complete
matrix:

```json
{
  "preferences": [
    {"event_type": "new_comment", "channel": "in_app", "enabled": false}
  ]
}
```

A notification in another org returns 404 even when the id exists, matching the
tenant-isolation rule used elsewhere. Invalid cursors return `400
invalid_cursor`; malformed notification ids return `400 invalid_id`.

### Satisfaction Ratings

When a ticket transitions to `solved`, the outbox enqueues the email consumer
only for that transition. The requester receives a `ticket_solved` email with a
signed link to `${PUBLIC_APP_URL}/rate?token=...`. Other status transitions,
including reopen and auto-close, send no customer email.

`POST /v1/public/ratings` is unauthenticated because the signed token is the
credential:

```json
{"token": "<signed token>", "score": 5, "comment": "Fast and helpful"}
```

- A token is `base64url(JSON{v,t,c,e}) + "." + hex(HMAC-SHA256(secret,
  encoded))`. It carries the ticket id, requester id and expiry, is signed with
  the dedicated `RATING_TOKEN_SECRET`, and is compared in constant time.
- Expired tokens return `422 expired_token`; tampered tokens and tokens whose
  ticket no longer exists both return `422 invalid_token`, so the endpoint
  cannot be used to probe ticket ids.
- `score` must be `1`–`5` and `comment` at most 2000 characters.
- `UNIQUE (ticket_id)` makes each ticket rateable exactly once; a replay returns
  `409 already_rated`.
- The endpoint has its own per-IP budget
  (`RATING_RATE_LIMIT_REQUESTS` / `RATING_RATE_LIMIT_WINDOW`) rather than
  sharing the signup bucket.
- A successful rating invalidates the org's dashboard cache, so the new
  `average_rating` and `rating_count` appear immediately.

Tokens default to a 30-day lifetime (`RATING_TOKEN_TTL`). The ticket's org is
always resolved from PostgreSQL, never trusted from the token.

### Webhooks

Webhooks let an org subscribe to ticket and comment events and receive
signed HTTP callbacks. The outbox is the durable source of truth: each
outbox event plans one delivery per active matching subscription, stores a
`webhook_deliveries` row, and enqueues a `webhook:deliver` task. The unique
`(subscription_id, event_id)` index makes replay converge on the existing
row instead of delivering twice.

| Method | Path | Purpose |
|:--|:--|:--|
| `GET` | `/v1/webhooks` | List the org's subscriptions (secret never returned) |
| `POST` | `/v1/webhooks` | Create a subscription (owner/admin only; returns the secret once) |
| `GET` | `/v1/webhooks/{id}` | Get one subscription (org-scoped, 404 for foreign orgs) |
| `PATCH` | `/v1/webhooks/{id}` | Partial update (owner/admin only); re-enabling resets the failure counter |
| `DELETE` | `/v1/webhooks/{id}` | Delete a subscription and its delivery log (owner/admin only) |
| `POST` | `/v1/webhooks/{id}/test` | Send one `webhook.ping` envelope synchronously (owner/admin only) |
| `GET` | `/v1/webhooks/{id}/deliveries` | Cursor-paginated delivery log, newest first |
| `POST` | `/v1/webhooks/{id}/deliveries/{deliveryId}/redeliver` | Manually reset and retry one delivery (owner/admin only) |

A subscription's URL must be an absolute `http` or `https` URL whose host
resolves only to public addresses. Loopback (`127.0.0.0/8`, `::1`), private
(`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`), link-local, CGNAT,
documentation, benchmarking, multicast, reserved and cloud-metadata ranges
(`169.254.0.0/16`) are rejected at creation. The HTTP client repeats the
address check at dial time through a custom `net.Dialer.Control`, so DNS
rebinding cannot slip past, never uses a proxy (`Proxy: nil`), does not
follow redirects, and enforces a 10-second timeout per attempt.

Supported event types:
`ticket.created`, `ticket.assigned`, `ticket.status_changed`,
`ticket.closed`, `ticket.deleted`, `ticket.merged`, `ticket.tagged`,
`ticket.untagged`, `comment.added`, `sla.breached`.
`webhook.ping` exists only for the test endpoint and cannot be subscribed to.

Every delivery is an envelope:

```json
{
  "id": "550e8400-e29b-41d4-a716-446655440000",
  "type": "ticket.created",
  "created_at": "2026-01-15T10:00:00Z",
  "data": {
    "ticket": {"id":"...","number":1042,"subject":"Cannot log in","status":"open","priority":"normal","requester_id":"...","assignee_id":null},
    "event": {"number":1042,"subject":"Cannot log in"}
  }
}
```

The HTTP request carries:

- `X-Gregport-Event`: the event type (`ticket.created`, `webhook.ping`, …)
- `X-Gregport-Delivery`: the delivery UUID
- `X-Gregport-Timestamp`: Unix seconds
- `X-Gregport-Signature`: `hex(HMAC-SHA256(secret, timestamp + "." + body))`
- `X-Gregport-Attempt`: attempt number, starting at 1

Receivers should verify the signature with constant-time comparison and
reject timestamps older than five minutes. The delivery body is the exact
bytes that were signed.

Each subscription has a server-generated `whsec_` + 32-byte-hex secret, returned
only on `POST /v1/webhooks`. List/get responses never include it.

Delivery reliability:

- Any 2xx is a success; anything else (non-2xx, transport error, redirect,
  timeout) is a failure.
- Failed deliveries are retried on the schedule `1m, 5m, 30m, 2h, 6h, 12h`,
  seven total attempts over roughly one day, then marked `dead`.
- After 10 consecutive failures the subscription is disabled and each org
  owner/admin receives one in-app `webhook_disabled` notification. Manual
  re-enable (`PATCH` with `is_active: true`) resets the counter.
- Test pings and manual redeliveries do not count toward the circuit breaker.
- Disabled subscriptions return `409 webhook_inactive` on test.

The delivery log records `status`, `attempts`, `response_status`, a
truncated `response_body`, `last_error`, `next_retry_at`, `delivered_at` and
`duration_ms`, paginated with `limit` (default 50, max 200) and an opaque
`cursor`.

Migration `0007_webhooks` adds `consecutive_failures`, `last_failure_at` and
`disabled_at` to `webhook_subscriptions`, plus `response_body` and
`duration_ms` to `webhook_deliveries`, the partial unique index on
`(subscription_id, event_id)`, the subscription-created index, and `ON DELETE
CASCADE` on deliveries.

Worker configuration shares the webhook queue with other I/O work, and the
webhook delivery tasks use the slower `webhook:deliver` retry schedule.

### Inbound Email

Inbound email lets customers create tickets and reply to existing tickets by
sending email to the organization's support address. The provider posts the
raw message to `POST /v1/inbound/email`, which is unauthenticated by session
but protected by an HMAC signature:

- `X-Inbound-Email-Timestamp`: Unix seconds when the provider signed.
- `X-Inbound-Email-Signature`: `hex(HMAC-SHA256(INBOUND_EMAIL_SECRET, timestamp + "." + body))`, verified in constant time with a five-minute timestamp tolerance.

Each message is parsed (MIME headers and `text/plain` body), then routed:

- Subject containing `[#1042]` → appends a public customer comment to ticket 1042.
- `In-Reply-To` referencing a previously received message → appends a comment to that message's ticket.
- Otherwise → creates a new ticket (`channel: "email"`) with the message as the first comment.

The sender is upserted as a customer of the org (`customers.org_id, email`), so a reply from a known address threads correctly. Every received message is recorded in `inbound_emails` with a unique `message_id`, so replaying the same message is idempotent (a duplicate returns `202` without creating a second ticket or comment). The raw email is stored for replay and audit.

The recipient address local part carries the org UUID (e.g. `support+{orgID}@inbound.example.com` or `{orgID}@inbound.example.com`), which resolves the tenant.

Migration `0008_inbound_email` creates the `inbound_emails` table (`message_id` unique, `org_id`, `ticket_id`, `comment_id`, `raw`, `received_at`) with indexes on `(org_id, received_at DESC)` and `message_id`.

---

## Tech Stack

| Component | Technology |
|:--|:--|
| Language | Go 1.27 |
| Router | [chi](https://github.com/go-chi/chi) |
| Database | PostgreSQL 16 |
| Cache and queue backend | Redis 7 |
| Queue | [hibiken/asynq](https://github.com/hibiken/asynq) |
| Circuit breaker | [sony/gobreaker](https://github.com/sony/gobreaker) |
| Email transport | Standard library `net/smtp` |
| Database environment | Docker Compose |
| Container image | Multi-stage Docker build on Alpine |
| PostgreSQL driver | [pgx/v5](https://github.com/jackc/pgx) |
| Authentication | bcrypt, [golang-jwt/jwt/v5](https://github.com/golang-jwt/jwt) |
| IDs | [google/uuid](https://github.com/google/uuid) |
| Env loading | [godotenv](https://github.com/joho/godotenv) |

---

## Architecture

Gregport follows a layered architecture. Each feature lives in its own package under `internal/`, containing its models, repository, service, and handler.

The dependency direction is:

```text
handler → service → repository → database
```

Each layer has a defined responsibility:

- **Handler:** Handles HTTP requests, JSON decoding, and responses.
- **Service:** Contains business logic and remains independent of HTTP and SQL.
- **Repository:** Handles database queries and persistence.
- **Database:** Provides shared database infrastructure, connection pooling, and transactions.

The service layer depends on repository interfaces rather than concrete implementations. This allows tests to inject fake repositories without requiring a database.

### Project Structure

<details>
<summary><code>.github/workflows/</code> — continuous integration</summary>

- `ci.yml` — PostgreSQL-backed tests, compose validation, image build, and container smoke test.

</details>

<details>
<summary><code>Dockerfile</code> and <code>docker-compose.yml</code> — container build and local stack</summary>

- `Dockerfile` — multi-stage build producing a non-root Alpine runtime image for the API.
- `Dockerfile.worker` — the same build for the background worker image.
- `docker-compose.yml` — PostgreSQL, Redis, the API and the worker, with health checks and migrations.
- `.dockerignore` — keeps VCS metadata, secrets and docs out of the build context.

</details>

<details>
<summary><code>cmd/api/</code> — application entry point</summary>

- `main.go` — application initialization and entry point.

</details>

<details>
<summary><code>cmd/worker/</code> — background worker entry point</summary>

- `main.go` — worker initialization: config, logger, PostgreSQL, Redis, health server, the I/O and CPU asynq servers, the outbox dispatcher, the breach/auto-close/housekeeping schedulers and the email handler.
- `server.go` — health HTTP server with the same signal-driven graceful shutdown as the API.

</details>

<details>
<summary><code>internal/auth/</code> — authentication and tokens</summary>

- `models.go`
- `repo.go`
- `service.go`
- `handler.go`
- `errors.go`
- `tokens.go`
- `repository_tokens_test.go`

</details>

<details>
<summary><code>internal/dashboard/</code> — dashboard reads</summary>

- `models.go` — ticket count summary and breakdowns.
- `repository.go` — single aggregate query scoped by `org_id`.
- `cached_store.go` — read-through counts cache and ticket-write invalidator.
- `service.go` — service boundary for the cached implementation.
- `handler.go` — `GET /v1/dashboard/counts`.

</details>

<details>
<summary><code>internal/idempotency/</code> — idempotency records</summary>

- `models.go` — acquire outcomes, stored record, TTL and key limits, and errors.
- `repository.go` — atomic acquire/complete/release/purge against `idempotency_keys`.

</details>

<details>
<summary><code>internal/organizations/</code> — organization settings</summary>

- `models.go` — typed settings document, partial update request, and errors.
- `repository.go` — JSONB read/write against `organizations.settings`.
- `cached_store.go` — read-through Redis decorator and update invalidation.
- `service.go` — defaults, merging, normalisation, and validation.
- `handler.go` — `GET` and `PATCH /v1/organization/settings`.

</details>

<details>
<summary><code>internal/sla/</code> — SLA policies</summary>

- `models.go` — policy entity, request/response DTOs, and errors.
- `engine.go` — pure wall-clock deadline calculator behind a `Calculator` interface.
- `repository.go` — org-scoped CRUD and unique/FK error mapping.
- `cached_store.go` — read-through caches for lists and items with invalidation.
- `service.go` — normalisation and validation rules.
- `handler.go` — `/v1/sla-policies` endpoints.

</details>

<details>
<summary><code>internal/tickets/</code> — ticket domain</summary>

- `models.go`
- `repository.go`
- `service.go`
- `handler.go`
- `transitions.go`
- `events.go`
- `pagination.go`
- `policy.go`
- `errors.go`
- `repository_test.go`
- `pagination_test.go`
- `update_test.go`
- `transitions_test.go`
- `tenant_test.go`
- `authz_test.go`
- `rollback_test.go`
- `assignment_test.go`
- `teams_test.go`
- `testhelpers_test.go` — shared test helpers, including `seedMembership`, `freshOrg`, and `dropOrg`.

</details>

<details>
<summary><code>internal/middleware/</code> — HTTP middleware</summary>

- `idempotency.go` — `Idempotency-Key` middleware backed by the PostgreSQL idempotency store.
- `ratelimit.go` — per-user and per-IP rate limiting with 429 headers.
- `requestid.go`
- `recovery.go`
- `timeout.go`

</details>

<details>
<summary><code>migrations/</code> — database migrations</summary>

- SQL schema and migrations.

</details>

<details>
<summary><code>internal/platform/</code> — shared infrastructure</summary>

- `audit/` — `Record(ctx, tx, orgID, principal, action, entityType, entityID, data)` for the append-only audit log.
- `cache/` — Redis-backed `Cache` interface, tenant-scoped key builders, and an in-memory test double.
- `clientip/` — client-IP resolution with opt-in trusted-proxy handling.
- `config/` — configuration loading from environment variables.
- `database/` — pgx connection pool and transaction management.
- `httpx/` — `DecodeJSON`, `WriteJSON`, `WriteError`, and `APIError`.
- `logging/` — JSON `slog` construction, log-level parsing, and the request-scoped logger in the context.
- `metrics/` — process-local counters for cache, rate-limit, and idempotency outcomes.
- `ratelimit/` — atomic Redis sliding-window limiter and the in-memory login fallback.
- `requestctx/` — authenticated principal stored in request context, `Kind` (user | api_key) and `ActorID`.

</details>

<details>
<summary><code>internal/queue/</code> — Redis queue</summary>

- `queue.go` — asynq task types, queue weights, client, deterministic `Job` enqueueing, servers and the slog/error adapters.
- `retry.go` — exponential backoff with ±20% jitter, capped at one hour.
- `inspector.go` — queue snapshots and the dead-letter (archived task) view with retry/delete helpers.

</details>

<details>
<summary><code>internal/outbox/</code> — transactional outbox</summary>

- `dispatcher.go` — claims `ticket_events` with `FOR UPDATE SKIP LOCKED`, maps event types to consumers and marks rows dispatched.

</details>

<details>
<summary><code>internal/housekeeping/</code> — retention-based pruning</summary>

- `housekeeping.go` — runs the configured purge funcs (idempotency keys,
  dispatched ticket events, audit logs, email deliveries) bounded by batch.

</details>

<details>
<summary><code>internal/email/</code> — email delivery</summary>

- `models.go` — delivery payload, planned and persisted delivery, templates and statuses.
- `repository.go` — loads ticket/comment context, persists delivery rows, marks sent/failed/dead.
- `service.go` — plans recipients per event type, idempotent via the unique `(event_id, to_email, template)` index.
- `sender.go` — `Sender` interface and `CircuitBreaker` backed by `sony/gobreaker`.
- `smtp.go` — `SMTPSender` delivering over SMTP via the standard library.
- `handler.go` — asynq `email:send` task handler; plans, sends, and records outcomes.

</details>

<details>
<summary><code>internal/ratings/</code> — satisfaction ratings</summary>

- `token.go` — HMAC-signed, expiring rating tokens and link building.
- `models.go` — rating, ratable ticket, request/response DTOs and errors.
- `repository.go` — ticket ownership lookup, unique insert and org average.
- `service.go` — score/comment validation, token verification and cache invalidation.
- `handler.go` — public `POST /v1/public/ratings`.

</details>

<details>
<summary><code>internal/worker/</code> — worker runtime</summary>

- `health.go` — `/healthz` and `/readyz` (PostgreSQL, Redis cache and queue).
- `handlers.go` — asynq handler registration.
- `queue_stats.go` — periodic queue/dead-letter snapshot logging.

</details>

---

## Database Schema

![Entity Relationship Diagram](docs/ERD.svg)

Migrations live in `migrations/`. The Compose stack applies the `*.up.sql`
files in order when the database volume is first created.

`0004_worker_sla_email` prepares the background pipeline:

- `ticket_events.dispatched_at` (partial index where it is `NULL`) marks the
  outbox rows the dispatcher still has to enqueue.
- `tickets.first_response_breached_at` and `tickets.resolution_breached_at`
  record the one-time breach markers, with partial indexes on the due columns.
- `email_deliveries.event_id` plus a unique index over
  `(event_id, to_email, template)` makes email delivery idempotent.

`0005_notifications_dedupe` links notifications to their source event:

- `notifications.event_id` references `ticket_events(id)`.
- A partial unique index over `(event_id, user_id) WHERE event_id IS NOT NULL`
  makes notification fan-out idempotent under replay.
- `idx_webhook_deliveries_retry` supports polling pending webhook deliveries by
  `(status, next_retry_at)`.

`0006_ticket_ratings` stores public satisfaction ratings:

- `ticket_ratings` holds `org_id`, `ticket_id`, `customer_id`, a `1`–`5` score
  and an optional comment.
- `UNIQUE (ticket_id)` makes the rating link single-use per ticket.
- `idx_ticket_ratings_org_created` supports the org-scoped average and count.

---

## API Reference

<details open>
<summary><b>Interactive docs</b></summary>

- **View spec (Swagger Editor):** [Open `docs/swagger.yaml`](https://editor.swagger.io/?url=https://raw.githubusercontent.com/HenriNorman/gregport/main/docs/swagger.yaml)

</details>

The OpenAPI spec lives at [`docs/swagger.yaml`](./docs/swagger.yaml).

---

## Getting Started

### Prerequisites

- Go 1.27
- Docker and Docker Compose
- PostgreSQL 16 (provided through Docker Compose)

### Installation

Clone the repository and enter the project directory.

Copy the example environment file:

```bash
cp .env.example .env
```

Configure the required environment variables, especially `DB_PASSWORD` and `JWT_SECRET`.

#### Full Stack with Compose

```bash
docker compose up --build -d --wait
```

This builds the API image, starts PostgreSQL, applies the migrations on the
first database initialization, and waits for both health checks. The API is
available at `http://localhost:8080`.

```bash
docker compose logs -f api
curl localhost:8080/healthz
```

The worker is a separate process with its own health probes:

```bash
docker compose logs -f worker
curl localhost:8081/healthz
curl localhost:8081/readyz
```

Stop the stack with `docker compose down`. To delete the database volume and
re-apply migrations from scratch:

```bash
docker compose down -v
```

#### Local Go Process

Start only PostgreSQL:

```bash
docker compose up -d db --wait
```

Then run the API on the host:

```bash
go run ./cmd/api
```

The API listens on port `8080` by default. You can override this using `APP_PORT`.

### Health Checks

Liveness and readiness are separate so a database outage does not restart the
process.

```bash
curl localhost:8080/healthz
curl localhost:8080/readyz
```

`/healthz` is a liveness probe. It returns `200` with `{"status":"ok"}` when
the process can serve HTTP. It does not touch PostgreSQL.

`/readyz` is a readiness probe. It pings PostgreSQL and Redis with a
two-second deadline:

```json
{"status":"ok"}
```

A `200` means the API can serve requests. If either ping fails, it returns `503`:

```json
{"status":"unready"}
```

The database error is logged at `WARN` with the request ID but never returned
to the caller.

### Container Image

Build the production image:

```bash
docker build -t gregport-api .
```

The build has two stages:

- `golang:1.27.2-alpine` compiles a static Linux binary with `CGO_ENABLED=0`,
  `-trimpath`, and stripped symbols.
- `alpine:3.24` contains only that binary and runs it as the non-root `app`
  user (UID 10001).

The container listens on `8080`, logs JSON to stdout, and handles `SIGTERM` so
graceful shutdown works. Set `APP_PORT` to change the port. When running the
container, point `DB_HOST` at a PostgreSQL instance reachable from inside the
container; `localhost` refers to the container itself.

### Docker Compose Stack

`docker compose up --build -d --wait` starts the core services on the
`gregport` network:

| Service | Purpose |
|:--|:--|
| `db` | `postgres:16-alpine` with a named `pgdata` volume and a `pg_isready` health check. |
| `redis` | `redis:7-alpine` with a `redis-cli ping` health check. Cache and limiter state are intentionally not persisted. |
| `api` | Built from the local `Dockerfile`; waits for `db` and `redis` to be healthy and reads its environment from `.env`. |
| `worker` | Built from `Dockerfile.worker`; connects to the same database and Redis, exposes its health probes on `${WORKER_PORT:-8081}`, and has a 40s stop grace period for in-flight jobs. |
| `asynqmon` | Optional queue UI from `hibiken/asynqmon`. It lives in the `ops` profile and is not started by default. |

- PostgreSQL publishes `${DB_PORT:-5432}` on the host; the API connects
  internally on `db:5432`, regardless of the host port.
- Redis publishes `${REDIS_PORT:-6379}` on the host; the API connects
  internally on `redis:6379`.
- The `*.up.sql` migrations are mounted read-only into
  `/docker-entrypoint-initdb.d` and run once when the volume is first created.
  Use `docker compose down -v` to reset the database.
- The API publishes `${APP_PORT:-8080}` and is health-checked through
  `/healthz`.
- `stop_grace_period` is `20s`, above the default `SHUTDOWN_TIMEOUT` of `15s`,
  so Docker waits for graceful shutdown instead of killing the process early.
- Asynqmon publishes `${ASYNQMON_PORT:-8082}` when the `ops` profile is enabled:

  ```bash
  docker compose --profile ops up -d asynqmon
  open http://localhost:8082
  ```

  It runs with `--read-only`, so the UI can inspect queues and archived tasks
  but cannot retry or delete them. Remove that flag for an operator session
  that needs to requeue dead letters. The UI has no authentication: keep it on
  a private network and never expose it publicly.
- Compose refuses to start if `DB_PASSWORD` or `JWT_SECRET` is missing.

---

## Configuration

The application reads its configuration from environment variables.

| Variable | Required | Default | Notes |
|:--|:--:|:--|:--|
| `DB_HOST` | No | `localhost` | PostgreSQL host |
| `DB_PORT` | No | `5432` | PostgreSQL port |
| `DB_USER` | No | `postgres` | Database user |
| `DB_PASSWORD` | Yes | — | Database password |
| `DB_NAME` | No | `gregport` | Database name |
| `APP_PORT` | No | `8080` | HTTP server port |
| `LOG_LEVEL` | No | `info` | `debug`, `info`, `warn`, or `error` |
| `JWT_SECRET` | Yes | — | Minimum 32 bytes |
| `RATING_TOKEN_SECRET` | Yes | — | Minimum 32 bytes; signs public rating links, keep separate from `JWT_SECRET` |
| `RATING_TOKEN_TTL` | No | `720h` | Rating-link lifetime |
| `PUBLIC_APP_URL` | No | `http://localhost:8080` | Absolute http(s) base URL used in rating links |
| `ACCESS_TOKEN_TTL` | No | `15m` | Go duration |
| `REFRESH_TOKEN_TTL` | No | `720h` | Go duration (30 days) |
| `SERVER_READ_HEADER_TIMEOUT` | No | `5s` | Go duration |
| `SERVER_READ_TIMEOUT` | No | `15s` | Go duration |
| `SERVER_WRITE_TIMEOUT` | No | `30s` | Go duration |
| `SERVER_IDLE_TIMEOUT` | No | `60s` | Go duration |
| `REQUEST_TIMEOUT` | No | `10s` | Go duration; keep below `SERVER_WRITE_TIMEOUT` |
| `SHUTDOWN_TIMEOUT` | No | `15s` | Go duration; keep above `REQUEST_TIMEOUT` |
| `REDIS_ADDR` | No | `localhost:6379` | Redis host and port |
| `REDIS_PASSWORD` | No | — | Redis password |
| `REDIS_DB` | No | `0` | Redis logical database |
| `REDIS_DIAL_TIMEOUT` | No | `1s` | Go duration |
| `REDIS_READ_TIMEOUT` | No | `500ms` | Go duration |
| `REDIS_WRITE_TIMEOUT` | No | `500ms` | Go duration |
| `CACHE_TIMEOUT` | No | `200ms` | Per-operation cache deadline |
| `CACHE_TTL_SETTINGS` | No | `5m` | Org settings cache TTL |
| `CACHE_TTL_SLA_POLICIES` | No | `5m` | SLA policy cache TTL |
| `CACHE_TTL_DASHBOARD_COUNTS` | No | `45s` | Dashboard counts cache TTL |
| `RATE_LIMIT_REQUESTS` | No | `100` | Authenticated requests per window |
| `RATE_LIMIT_WINDOW` | No | `1m` | Authenticated rate-limit window |
| `PUBLIC_RATE_LIMIT_REQUESTS` | No | `30` | Unauthenticated requests per window |
| `PUBLIC_RATE_LIMIT_WINDOW` | No | `1m` | Unauthenticated rate-limit window |
| `LOGIN_RATE_LIMIT_REQUESTS` | No | `5` | Login attempts per window |
| `LOGIN_RATE_LIMIT_WINDOW` | No | `15m` | Login rate-limit window |
| `RATING_RATE_LIMIT_REQUESTS` | No | `5` | Public rating submissions per window |
| `RATING_RATE_LIMIT_WINDOW` | No | `15m` | Public rating rate-limit window |
| `TRUST_PROXY_HEADERS` | No | `false` | Honour `X-Forwarded-For` only when explicitly enabled |
| `TRUSTED_PROXY_CIDRS` | No | — | Comma-separated trusted proxy CIDRs |
| `IDEMPOTENCY_TTL` | No | `24h` | How long an idempotency key is retained; used by the API middleware and housekeeping |
| `WORKER_PORT` | No | `8081` | Worker health/metrics port |
| `WORKER_CONCURRENCY` | No | `20` | I/O worker concurrency, shared by the `critical`/`default`/`low` queues |
| `WORKER_CPU_CONCURRENCY` | No | number of CPUs | Concurrency of the CPU-bound queue server |
| `WORKER_SHUTDOWN_TIMEOUT` | No | `30s` | Time allowed to drain in-flight jobs |
| `WORKER_READ_HEADER_TIMEOUT` | No | `5s` | Worker health server timeout |
| `WORKER_READ_TIMEOUT` | No | `10s` | Worker health server timeout |
| `WORKER_WRITE_TIMEOUT` | No | `10s` | Worker health server timeout |
| `WORKER_IDLE_TIMEOUT` | No | `60s` | Worker health server idle timeout |
| `WORKER_OUTBOX_INTERVAL` | No | `1s` | How often the outbox dispatcher polls |
| `WORKER_OUTBOX_BATCH` | No | `100` | Maximum events claimed per outbox transaction |
| `WORKER_QUEUE_STATS_INTERVAL` | No | `1m` | Queue snapshot interval; `0` disables the snapshot log |
| `SMTP_HOST` | No | `localhost` | Outgoing SMTP host |
| `SMTP_PORT` | No | `2525` | Outgoing SMTP port |
| `SMTP_USER` | No | — | SMTP auth username |
| `SMTP_PASSWORD` | No | — | SMTP auth password |
| `SMTP_FROM` | No | `no-reply@localhost` | Envelope `From` address |
| `SMTP_FROM_NAME` | No | `Gregport` | Display name for the `From` header |
| `SLA_BREACH_CHECK_INTERVAL` | No | `1m` | SLA breach check interval; `0` disables the scheduler |
| `SLA_BREACH_CHECK_BATCH` | No | `100` | Maximum tickets checked per breach run |
| `AUTO_CLOSE_WINDOW` | No | `168h` | How long a solved ticket ages before being auto-closed; `0` closes immediately |
| `AUTO_CLOSE_CHECK_INTERVAL` | No | `1h` | Auto-close check interval; `0` disables the scheduler |
| `AUTO_CLOSE_CHECK_BATCH` | No | `100` | Maximum tickets closed per auto-close run |
| `HOUSEKEEPING_INTERVAL` | No | `1h` | Housekeeping run interval; `0` disables the scheduler |
| `HOUSEKEEPING_BATCH` | No | `100` | Maximum rows deleted per housekeeping purge |
| `TICKET_EVENT_RETENTION` | No | `720h` | How long dispatched ticket events are retained; `0` disables the purge |
| `AUDIT_LOG_RETENTION` | No | `2160h` | How long audit log rows are retained; `0` disables the purge |
| `EMAIL_DELIVERY_RETENTION` | No | `720h` | How long sent or dead email deliveries are retained; `0` disables the purge |

Configuration is validated at startup, before the database is opened. Missing
or invalid values make the process exit non-zero, and every problem found is
reported together. In particular:

- `DB_PASSWORD` is required and has no default.
- `JWT_SECRET` must be at least 32 bytes.
- `RATING_TOKEN_SECRET` must be at least 32 bytes; `PUBLIC_APP_URL` must be an
  absolute `http` or `https` URL.
- `LOG_LEVEL` must be `debug`, `info`, `warn`, or `error`.
- Ports must be between `1` and `65535`.
- Durations must be positive unless the variable is documented as `0`-disablable; `REQUEST_TIMEOUT=0` disables the deadline.
- `REQUEST_TIMEOUT` must be below `SERVER_WRITE_TIMEOUT`.
- `SHUTDOWN_TIMEOUT` must be at least `REQUEST_TIMEOUT`.
- Redis timeouts and cache TTLs must be positive; `REDIS_DB` must be zero or greater.
- Rate-limit request counts must be at least one; windows must be positive.
- `TRUST_PROXY_HEADERS=true` requires at least one valid `TRUSTED_PROXY_CIDRS` entry.
- `IDEMPOTENCY_TTL` must be positive.
- The worker reads the shared database/Redis variables plus the `WORKER_*`
  knobs. It does not require `JWT_SECRET`, because it never authenticates users,
  but it does require `RATING_TOKEN_SECRET` so solved-ticket email can sign
  rating links.
- The database DSN is built with `url.URL`, so characters such as `@`, `:`,
  `/`, `?`, `#` and `%` in credentials are escaped automatically.

Generate a secure JWT secret using:

```bash
openssl rand -base64 32
```

`JWT_SECRET` has no default. The application terminates at startup if it is missing or invalid.

Never commit the secret, log it, or reuse it across projects.

---

## Caching

`internal/platform/cache` exposes a small `Get`/`Set`/`Delete` interface and a
single Redis implementation. Services depend on the interface, so tests use
the in-memory `Memory` implementation without a Redis process.

Every cache key for tenant data uses one scheme and always contains the org id:

```text
org:{orgID}:settings
org:{orgID}:sla_policies
org:{orgID}:sla_policy:{policyID}
org:{orgID}:dashboard:counts
org:{orgID}:apikey:{apiKeyID}:last_used
```

Authenticated rate-limit buckets also include the org id. The only keys without
a tenant are the pre-authentication public-IP and login buckets, because no
tenant is known yet.

The key builders return an error when the org id is missing. A test enumerates
every builder and fails if the org id is absent or two keys collide.

### Read-through Caching

Caching is applied at the repository boundary by a decorator
(`organizations.CachedStore`, `sla.CachedStore`, `dashboard.CachedStore`) so
services and handlers stay unaware of Redis.

| Data | Key | TTL | Invalidation |
|:--|:--|:--|:--|
| Organization settings | `org:{orgID}:settings` | `CACHE_TTL_SETTINGS` (5m) | Deleted after a successful `PATCH /v1/organization/settings` |
| SLA policy list | `org:{orgID}:sla_policies` | `CACHE_TTL_SLA_POLICIES` (5m) | Deleted after a successful create, update, or delete |
| SLA policy item | `org:{orgID}:sla_policy:{policyID}` | `CACHE_TTL_SLA_POLICIES` (5m) | Deleted after a successful update or delete |
| Dashboard counts | `org:{orgID}:dashboard:counts` | `CACHE_TTL_DASHBOARD_COUNTS` (45s) | Deleted after a successful ticket create, update, delete, assign, or merge |

- A miss reads PostgreSQL and writes the result to Redis. Hits return the
  serialized document without touching the database.
- Only successful reads are cached. `404` responses are never memoized.
- Corrupt cache entries are treated as misses and overwritten.
- A read or write failure in Redis falls through to PostgreSQL.
- Invalidation happens after the database write commits. If the delete fails,
  the stale entry expires at the TTL rather than blocking the update.
- SLA list and item caches are independent: reading a list does not populate
  every item, and a single update invalidates both affected keys.
- Dashboard counts use a deliberately short TTL. Ticket mutations evict the
  key immediately; the TTL bounds staleness if an invalidation is lost while
  Redis is unavailable, and an invalidation failure is logged, never surfaced
  to the caller.

### Redis Failure Policy

Redis is an optimization and a protection layer, not the source of truth.
Behaviour is chosen per path, not globally:

| Path | Redis unavailable | Reason |
|:--|:--|:--|
| General API rate limits | Fail open, log, and count | A cache outage must not become an API outage. |
| Login rate limit | Fall back to a stricter in-memory limiter | Credential stuffing should not be made easier by an outage. |
| Read-through cache | Bypass Redis and read PostgreSQL | Correctness wins; the request still completes. |
| Idempotency | Unaffected | Records live in PostgreSQL. |
| `/readyz` | Report `503` | Readiness exposes the degraded dependency. |

A readiness failure is intentional even though fail-open requests may still
succeed. Deployment tooling should treat it as a signal, not as proof that the
process cannot serve traffic.

---

## Rate Limiting

The limiter is a Redis sorted-set sliding window evaluated by one Lua script.
Removing expired entries, counting the window, and recording the current
request happen atomically on the Redis server, so concurrent requests cannot
both slip past the boundary. Redis `TIME` is used instead of the application
clock.

| Scope | Default | Window | Key |
|:--|:--|:--|:--|
| Authenticated user | 100 | 1 minute | `org:{orgID}:ratelimit:user:{userID}` |
| API key | 100 | 1 minute | `org:{orgID}:ratelimit:apikey:{keyID}` |
| Public IP | 30 | 1 minute | `ratelimit:ip:{ip}` |
| Login | 5 | 15 minutes | `ratelimit:login:{ip}:{emailHash}` |

The login bucket uses a SHA-256 hash of the normalised email, never the raw
address. When Redis is unavailable, login falls back to the in-memory
fixed-window limiter with half the configured limit; the general API fails
open. Fallback responses carry `X-RateLimit-Degraded: 1`.

Every limited response carries:

| Header | Meaning |
|:--|:--|
| `X-RateLimit-Limit` | Requests allowed in the current window |
| `X-RateLimit-Remaining` | Requests left after this one |
| `X-RateLimit-Reset` | Window reset time as a Unix timestamp |
| `Retry-After` | Seconds to wait; present on `429` only |

A rejected request returns the standard error envelope with code
`rate_limited` and status `429`.

The login limiter runs before the handler, buffers the request body, and
restores it. The public IP limiter covers signup, refresh, logout, and
invitation acceptance. `X-Forwarded-For` is ignored unless
`TRUST_PROXY_HEADERS=true` and the immediate peer matches
`TRUSTED_PROXY_CIDRS`.

---

## Idempotency

Idempotency records live in PostgreSQL, not Redis, so a cache outage cannot
replay or duplicate a write. `internal/idempotency` exposes the persistence
layer; request wiring is added on top.

Records are keyed by `(org_id, key)` and store a SHA-256 hash of the request
that first claimed the key:

| Stored state | `Acquire` outcome | Meaning |
|:--|:--|:--|
| No row, or row expired | `acquired` | This caller owns the key; the response is not stored yet |
| Same hash, response pending | `pending` | Another request with the same key is still in flight |
| Same hash, response stored | `replay` | Return the captured status and body unchanged |
| Different hash | `conflict` | The key was already used for a different request |

- `Acquire` replaces an expired row in one atomic upsert, so a client can reuse
  a key after `IDEMPOTENCY_TTL` without a cleanup race.
- `Complete` stores the status and JSON body, but only while the record is
  pending, unexpired, and owned by the same request hash.
- `Release` deletes a pending record after a failed request so a retry can use
  the same key. Releasing an absent or completed record is a no-op.
- `PurgeExpired` removes rows at or before a supplied time; the TTL default is
  `24h`.
- A key is capped at 255 bytes and a request hash at 128 bytes. Keys are
  trimmed and scoped to the caller's org, so two tenants can use the same
  client key independently.

### HTTP Middleware

`internal/middleware.Idempotency` enforces idempotency at the HTTP layer. It is
wired to all authenticated POST endpoints (ticket create, comment, assignment,
merge, macro apply, teams, tags, macros, invitations, API keys, SLA policies):

- The client sends `Idempotency-Key: <key>`. Requests without the header pass
  through unchanged.
- The key is scoped to the caller's organization, resolved from the
  authenticated principal. Unauthenticated requests receive `401`.
- The request hash is SHA-256 of the method, path, and body, so reusing a key
  with a different body returns `409 idempotency_conflict`.
- `acquired` → the handler runs and its status/body are stored via `Complete`.
- `replay` → the captured status and body are returned without calling the
  handler again.
- `pending` → `409 idempotency_pending`; another request with the same key is
  still in flight.
- `conflict` → `409 idempotency_conflict`.
- A panic releases the key so the client can retry, then re-panics for the
  recovery middleware.
- Store failures return `500` and never run the handler.

---

## Background Worker

`cmd/worker` is a second deployable binary. It has its own configuration
loader, JSON logger, PostgreSQL pool, Redis client, health probes and
signal-driven shutdown. The API and the worker share packages but never share
processes.

### Queue choice: asynq

The queue is [`hibiken/asynq`](https://github.com/hibiken/asynq), backed by the
Redis instance already used for caching, rate limiting and idempotency. The
alternatives were:

- **asynq** — retries, exponential backoff, scheduled tasks, priorities, an
  archived set (the dead-letter queue) and Asynqmon out of the box. Redis is
  already a dependency.
- **River** — a PostgreSQL queue that can enqueue inside the same transaction.
  That removes the dual-write gap, but it adds a second transactional code path
  and more database load. Our outbox already makes the event durable in
  PostgreSQL, so a Redis queue cannot lose an event: a crash between
  "enqueue" and "mark dispatched" produces a duplicate, and every consumer is
  idempotent by design.

asynq is the simplest fit for the current stack. The outbox remains the source
of truth; the queue is the delivery mechanism.

### Queues and retries

| Queue | Weight | Work |
|:--|--:|:--|
| `critical` | 6 | Email delivery |
| `default` | 3 | Breach checks, auto-close |
| `low` | 1 | Housekeeping |
| `cpu` | — | CPU-bound jobs (thumbnails, exports) on a separate server |

- Retries use exponential backoff from 5 seconds, doubling to a one-hour cap,
  with ±20% jitter so workers do not retry in synchronized waves.
- Permanent failures (for example an invalid email address) return
  `queue.Permanent(err)`, which wraps `asynq.SkipRetry` so the task is archived
  immediately instead of retried.
- Exhausted tasks stay in asynq's archived set, the dead-letter view. They are
  inspectable in Asynqmon and an `errorHandler` logs each retry and each
  exhaustion.
- I/O-bound jobs run on the `critical`/`default`/`low` server with
  `WORKER_CONCURRENCY` workers. CPU-bound jobs run on a second asynq server
  with `WORKER_CPU_CONCURRENCY` workers, near the core count, so they cannot
  occupy the I/O pool.

### Worker sizing: I/O vs CPU

The two servers are sized for different bottlenecks:

| Pool | Queues | Default concurrency | Sizing rule |
|:--|:--|--:|:--|
| I/O | `critical`, `default`, `low` | `20` | Can exceed the core count, because handlers mostly wait on PostgreSQL, Redis or SMTP |
| CPU | `cpu` | `runtime.NumCPU()` | Keep near the usable core count; more workers only add scheduling and cache pressure |

The isolation is structural, not a convention: the I/O server only consumes
`critical`/`default`/`low`, and the CPU server only consumes `cpu`. Queue
weights cannot route work across that boundary. A burst of CPU-bound tasks can
therefore fill every CPU worker without delaying email delivery or breach
checks. That property is pinned by two integration tests:

- `TestCPUSaturationDoesNotStarveIOQueue` fills the CPU pool with blocking
  tasks and asserts that I/O tasks still complete before the CPU work is
  released.
- `TestCPUServerEnforcesItsOwnConcurrency` asserts that a CPU burst never runs
  more handlers than `WORKER_CPU_CONCURRENCY`.

Measure queue overhead before sizing around it:

```bash
go test ./internal/queue -run '^$' -bench BenchmarkQueueRoundTrip -benchtime=200x
```

`BenchmarkQueueRoundTrip` reports enqueue-to-completion latency for a no-op
task on the I/O and CPU queues. In the in-process Redis used by the tests a
round trip is well under a millisecond, so handler work dominates; size the
pools for the handler, not the queue hop. On a real deployment, compare that
baseline with `WORKER_QUEUE_STATS_INTERVAL` snapshots (`pending`, `active`,
`latency`, `archived`) to decide whether a backlog needs more concurrency or
another worker host.

The two servers share one process and one Go runtime. The split prevents queue
starvation, not core contention: if CPU tasks would saturate every core, run
the CPU server on its own worker deployment as well.

### Outbox dispatcher

Ticket mutations write their `ticket_events` row in the same PostgreSQL
transaction as the ticket change. That row is the durable outbox: the API never
needs to talk to Redis to make an event safe.

The worker drains the outbox every `WORKER_OUTBOX_INTERVAL`:

1. Claim up to `WORKER_OUTBOX_BATCH` rows with
   `SELECT ... WHERE dispatched_at IS NULL ORDER BY created_at, id LIMIT $1
   FOR UPDATE SKIP LOCKED`. Two workers can poll concurrently without claiming
   the same row.
2. Map each event type to its consumers (`ticket.created`, `comment.added`,
   `ticket.assigned` and `sla.breached` fan out to both email and in-app
   notifications).
3. Enqueue each consumer with a deterministic task ID, `eventID:consumer`, so a
   repeated enqueue cannot create a second job. asynq reports that duplicate as
   `ErrTaskIDConflict`, which the client treats as success.
4. Mark the event's `dispatched_at` in the same transaction. If enqueueing
   fails the transaction rolls back, the events stay undispatched and the next
   poll retries them.

The result is **at-least-once** delivery. Redis and PostgreSQL cannot be
updated atomically, so a crash after enqueueing but before committing can leave
a job in Redis while the event is dispatched again later. Consumers therefore
check whether their effect already exists and treat a duplicate as success;
email uses the unique `(event_id, to_email, template)` index for this, and
notifications use the unique `(event_id, user_id)` index.
`DispatchOnce` keeps draining full batches so a backlog is cleared without
waiting one interval per batch. Unknown event types are marked dispatched
without producing a job, so they cannot pollute every poll.

### Queue visibility and dead letters

`internal/queue/inspector.go` wraps asynq's inspector with four operations:

- `Queues(ctx)` lists queues that currently hold state.
- `Stats(ctx, queues)` returns pending, active, scheduled, retry, archived,
  completed, paused, latency and memory figures.
- `DeadLetters(ctx, queue, page, pageSize)` lists archived tasks, newest
  failure first, with type, payload, retry count and last error.
- `RetryDeadLetter` and `DeleteDeadLetter` requeue or permanently remove one.

The worker logs a snapshot every `WORKER_QUEUE_STATS_INTERVAL` (default `1m`;
set `0` to disable). Snapshots containing archived tasks are logged at `WARN`,
because the archived set is the dead-letter queue: those tasks exhausted their
retries or returned `queue.Permanent`. Alerting on that warning surfaces
delivery problems without polling the UI.

For interactive inspection, start Asynqmon with the `ops` profile as shown in
the Compose section. Its Queue view shows pending, active, scheduled, retry and
archived tasks per queue; the Archived tab is the dead-letter view. Running the
UI read-only is the safe default; use `RetryDeadLetter`/`DeleteDeadLetter` or a
non-read-only Asynqmon session for operator actions.

## SLA Breach Scheduler

The worker runs a scheduled SLA breach check every `SLA_BREACH_CHECK_INTERVAL`
(default `1m`). It scans tickets whose `first_response_due_at` or
`resolution_due_at` has passed and marks them as breached:

- A ticket is flagged for a **first-response breach** when the first-response
  due date has passed, the agent has not yet responded (`first_response_at` is
  `NULL`), and the breach has not already been recorded.
- A ticket is flagged for a **resolution breach** when the resolution due date
  has passed, the ticket is not yet resolved (`resolved_at` is `NULL`), and the
  breach has not already been recorded.
- Soft-deleted tickets are skipped.

Each breach is handled atomically: the breach column is set and an
`sla.breached` ticket event is recorded in the same transaction. The outbox
dispatcher then fans the event out to the email consumer. Because the breach
column is the idempotency marker, running the check concurrently on multiple
workers never double-marks a breach: candidate rows are locked with
`FOR UPDATE SKIP LOCKED`.

Each scheduler run enqueues a single `sla:breach_check` task on the `default`
queue. The task ID is truncated to the current minute, so duplicate enqueues
within the same minute are deduplicated (asynq reports `ErrTaskIDConflict`,
treated as success), while each new minute gets a fresh task ID. The batch size
is controlled by `SLA_BREACH_CHECK_BATCH` (default `100`).

Set `SLA_BREACH_CHECK_INTERVAL=0` to disable the scheduler.

## Auto Close Scheduler

The worker runs a scheduled auto-close check every `AUTO_CLOSE_CHECK_INTERVAL`
(default `1h`). It scans tickets that have been in `solved` status for longer
than `AUTO_CLOSE_WINDOW` (default `168h`, i.e. 7 days) and transitions them to
`closed`, setting `closed_at`:

- Only solved tickets with `closed_at IS NULL` and `resolved_at` at or before
  `now() - window` are candidates.
- Each closure is handled atomically: `status` is set to `closed`, `closed_at`
  is stamped, and a `ticket.closed` ticket event is recorded in the same
  transaction.
- Soft-deleted and non-solved tickets are skipped.

Like the breach check, candidate rows are locked with `FOR UPDATE SKIP LOCKED`,
so multiple workers never close the same ticket. Each scheduler run enqueues a
single `ticket:auto_close` task on the `default` queue with a task ID truncated
to the current minute, so duplicate enqueues within the same minute are
deduplicated (asynq reports `ErrTaskIDConflict`, treated as success). The batch
size is controlled by `AUTO_CLOSE_CHECK_BATCH` (default `100`).

Set `AUTO_CLOSE_CHECK_INTERVAL=0` to disable the scheduler.

## Housekeeping Scheduler

The worker runs a scheduled housekeeping run every `HOUSEKEEPING_INTERVAL`
(default `1h`). It prunes rows that have outlived their retention window:

- **Idempotency keys**: expired keys (older than `IDEMPOTENCY_TTL`) are deleted
  so the idempotency store doesn't grow without bound.
- **Dispatched ticket events**: `ticket_events` rows that have been dispatched
  (`dispatched_at IS NOT NULL`) and are older than `TICKET_EVENT_RETENTION`
  are deleted. Events still referenced by an email delivery are skipped, and
  undispatched events (the outbox source of truth) are never purged.
- **Audit logs**: `audit_logs` rows older than `AUDIT_LOG_RETENTION` are deleted.
- **Email deliveries**: sent or dead deliveries older than
  `EMAIL_DELIVERY_RETENTION` are deleted. Pending and failed deliveries (which
  may still be retried) are kept.

Every purge is bounded by `HOUSEKEEPING_BATCH` (default `100`) rows per
statement, so a long retention window never blocks the worker. Purges are
independent: a failure in one is logged and does not abort the others. Each
scheduler run enqueues a single `housekeeping:run` task on the `low` queue with
a task ID truncated to the current minute, so duplicate enqueues within the
same minute are deduplicated.

Set `HOUSEKEEPING_INTERVAL=0` to disable the scheduler, or set any retention
window to `0` to disable that individual purge.

---

## Email Delivery

Email is delivered from the worker via the `email:send` queue task, which the
outbox dispatcher enqueues for `ticket.created`, `comment.added`,
`ticket.assigned`, `sla.breached` and solved-only `ticket.status_changed`
events. Delivery is idempotent:

- Planning persists one `email_deliveries` row per `(event_id, to_email,
  template)` before sending. The partial unique index on that triple means a
  replayed event returns the existing row instead of creating a second email.
- A `SMTPSender` delivers each email, wrapped in a `CircuitBreaker`
  (`sony/gobreaker`) that trips after `maxDeliveryAttempts` consecutive
  failures and probes again after `SMTP`-configured reset timeout. While the
  circuit is open, delivery fails fast without calling the mail provider.
- Send outcomes are recorded back to `email_deliveries`: `pending` → `sent`,
  `failed` (with `next_retry_at` and `last_error`), or `dead` after the attempt
  cap. Already-sent or dead deliveries are skipped on replay.
- Missing tickets or comments are terminal: the handler logs a warning and
  stops retrying instead of failing the task.

Customer-facing email is deliberately narrow: `ticket.created` notifies the
requester, and only an agent-authored **public** comment (`author_type = user`,
`is_internal = false`) is emailed back to the requester. Internal notes and
system notes never produce a customer email, and a customer reply is routed to
the assignee (or the org support inbox when unassigned). A transition to
`solved` sends the requester a `ticket_solved` email containing the signed
satisfaction-rating link; other status transitions produce nothing.

Every message is `multipart/alternative` with both a plain-text body rendered
from `text/template` and an HTML body rendered from `html/template`, so
customer-supplied subjects and comment bodies are escaped in the HTML part. The
subject always contains the ticket reference, for example `[#1042] Cannot log
in`, and customer-facing messages set `Reply-To` to the organization's
`settings.support_email` so replies land in the shared inbox. Header values are
stripped of CR/LF before they are written, preventing header injection through
a subject.

SMTP is configured with `SMTP_HOST`, `SMTP_PORT`, `SMTP_USER`,
`SMTP_PASSWORD`, `SMTP_FROM` and `SMTP_FROM_NAME`. Empty credentials mean no
SMTP AUTH.

---

## Notification Fan-out

In-app notifications are delivered by the `notify:send` queue task, enqueued by
the outbox dispatcher alongside the email job for the same event. Recipients
are resolved from the database, never trusted from the task payload:

- the ticket's current assignee;
- every user watching the ticket.

The acting user is always excluded, so an agent does not get a notification for
their own comment or assignment. The ticket requester is a customer, not a
user, so they do not receive an in-app notification; customer updates continue
to use the email path.

Each notification row stores the source `ticket_events.id`. A partial unique
index on `(event_id, user_id) WHERE event_id IS NOT NULL` plus
`ON CONFLICT DO NOTHING` makes replaying a notify job a no-op: a redelivered
event produces the same notification set, not duplicates.

Preferences default to **enabled** for every event type and channel. The
`notification_preferences` table stores only exceptions (disabled rows), which
keeps the common case read-free and makes new event types enabled by default.

---

## API Design and Rules

### Error Responses

Every failure uses one envelope:

```json
{"error":{"code":"ticket_not_found","message":"ticket not found","details":[],"request_id":"0f9b…"}}
```

- `code` is stable and machine-readable; `message` is safe to show to a user.
- `details` is always an array. Field-level validation errors populate it.
- `request_id` matches the `X-Request-ID` response header and the access-log
  line for the same request.
- Domain errors are `*httpx.APIError` sentinels, so each carries its own HTTP
  status, code and public message. `errors.Is` still works through wrapping.
- Unknown errors return `500` with `code: "internal"` and a generic message.
  The real error is logged at `ERROR` with the request ID and never sent to
  the client.
- JSON decoding failures return safe codes (`invalid_json`, `unknown_field`,
  `payload_too_large`) instead of raw decoder text.
- Panics are recovered, logged with their stack, and returned in the same
  envelope.

### Tenant Isolation

Tickets use per-organization numbers rather than UUIDs in their URLs.

The lookup key is `(org_id, number)`. A ticket belonging to another organization returns `404`, never `403`.

This prevents the API from confirming whether a resource exists in another tenant.

- **Authorization gates on `Kind` before role.** Every management gate in
  `policy.go` returns `false` for a principal whose `Kind` is not `user`,
  before checking the role. API keys are always `agent`-level and never
  manage teams, members, API keys, or webhooks. The grant table — who may
  invite whom — is a unit test: owner → admin/agent, admin → agent, nobody
  → owner.

- **Invitation tokens work like refresh tokens.** 32 random bytes from
  `crypto/rand`, base64url-encoded, returned **once** in the create
  response and never again — list responses never carry the token. Only the
  SHA-256 hash is stored in `invitations.token_hash`. Expiry is 7 days;
  single use is enforced by `accepted_at` under `FOR UPDATE`, so two
  simultaneous accepts of the same token can't both succeed.
- **Who can invite whom is a grant table, tested.** Owner → admin/agent.
  Admin → agent. Nobody → owner. API keys → nobody. Enforced by
  `CanInvite(principal, targetRole)`.
- **Accepting an invitation has two paths.** No principal on the request →
  new user: the body carries `name` and `password`, the user and membership
  are created in one transaction. A principal on the request → existing
  user: `name`/`password` are ignored, the principal's email must match the
  invitation's email (403 otherwise), and only the membership is created.
  A duplicate membership returns 409.

- **Member role changes reuse the invitation grant table.** `PATCH
  /v1/members/{userId}` calls `CanInvite(principal, newRole)` — owner may set
  admin or agent, admin may set agent only, nobody sets owner. A role change
  writes a `member.role_changed` audit row.
- **An org always keeps one owner.** `UpdateMemberRole` and `RemoveMember`
  lock the target membership `FOR UPDATE`, then lock every owner row in the
  org (`SELECT 1 FROM memberships WHERE role = 'owner' FOR UPDATE`), count
  them in Go, and refuse to demote or remove the last one (422). Two
  concurrent removals of two owners serialize on the owner-row locks: the
  first succeeds, the second sees one owner left and refuses. The invariant
  holds under concurrency, not just sequentially.
- **Removing a member revokes their refresh tokens for this org.**
  `DELETE /v1/members/{userId}` deletes the membership and runs `UPDATE
  refresh_tokens SET revoked_at = now() WHERE user_id = $1 AND org_id = $2`
  in the same transaction. The removed user's access token stays valid up to
  15 minutes (it's stateless), but their refresh token is dead immediately —
  they cannot get a new one. A `member.removed` audit row is written.
- **API keys are `gp_live_<id>_<secret>` and never stored raw.**
  `POST /v1/api-keys` generates 8 random bytes for the id and 32 for the
  secret, both base64url. The full key is returned once; the DB stores the
  prefix `gp_live_<id>` and the SHA-256 of the **full** key. Lookup is by
  prefix, then `subtle.ConstantTimeCompare` on the hash. List responses
  carry the prefix only.
- **Revoking a key sets `revoked_at`, it does not delete the row.** A
  revoked key returns 401 on every request — indistinguishable from an
  expired or unknown key to the client, but distinguishable in the DB, so
  "revoked" and "never existed" are not conflated.
- **API key mutations are owner/admin.** `CanManageAPIKeys` gates
  `POST/GET/DELETE /v1/api-keys`; the `RequireRole` middleware gates the
  routes. Keys themselves (the principals they authenticate) are never
  allowed to create, list, or revoke keys — see the policy rule.

- **`Authenticate` branches on the token prefix.** A bearer token starting
  with `gp_live_` takes the API-key path: parse the prefix, look up the row,
  reject revoked/expired, compare the SHA-256 of the full key with
  `subtle.ConstantTimeCompare`. Anything else takes the JWT path. Both paths
  produce a `Principal`; `Kind` distinguishes them.
- **`last_used_at` is throttled in Redis.** Each key's last write is gated by
  an atomic `SET NX ... EX` on `org:{orgID}:apikey:{apiKeyID}:last_used`, so
  the database is written at most once per minute per key, coordinated across
  API instances. Without Redis it falls back to a per-process in-memory map;
  when Redis is unavailable authentication continues and the database is not
  hammered.
- **API keys are agent-level and limited to tickets and comments.** They
  pass every ticket operation (create, list, get, update, comment, assign,
  tag, watch, merge, macro) and fail every management gate — members, teams,
  API keys, webhooks — because `policy.go` checks `Kind` before role.

- **Audit rows commit atomically with the mutation they describe.**
  `invitation.created`, `invitation.accepted`, `member.role_changed`,
  `member.removed`, `api_key.created`, and `api_key.revoked` each write an
  `audit_logs` row inside the same transaction as the change, via
  `audit.Record(ctx, tx, ...)`. The mutation methods that participate expose
  a `*Tx` variant that takes the caller's transaction; the service opens one
  transaction, calls the mutation and the audit, and commits once. A crash
  between the two cannot leave a mutation without its audit row.


### Optimistic Concurrency

Ticket updates require an `If-Match` header.

| Condition | HTTP Status |
|:--|:--|
| Missing `If-Match` header | `428 Precondition Required` |
| Stale ticket version | `412 Precondition Failed` |

Every successful ticket update increments its `version`.

This prevents two agents from silently overwriting each other's changes.

### Ticket Assignment

Assignment is validated within the same transaction as the ticket update.

- The assignee must have an organization membership with the `owner`, `admin`, or `agent` role.
- Team assignment verifies that the team belongs to the caller's organization.
- Client-provided user IDs are never trusted without membership verification.

Assignment also produces a `ticket.assigned` event containing the old and new assignee/team information.

The new assignee is automatically added to `ticket_watchers`. The insertion uses `ON CONFLICT DO NOTHING`, making repeated assignments idempotent.

### Round-robin Assignment

`auto: true` with a `team_id` picks the team member with the oldest `last_assigned_at`.

The pick runs inside the same transaction as the ticket update, with `FOR UPDATE OF team_members SKIP LOCKED` so concurrent auto-assigns to the same team do not both choose the same agent.

The join on `memberships` excludes agents removed from the org. A team with no assignable members returns `422`.

### Tags

- **Tags are lowercase and unique per org.** `POST /v1/tags` normalizes the name (trim, lowercase) before insert; a duplicate in the same org returns `409`. Attach and detach take the tag **name** in the URL, not the UUID, and normalize it the same way before the lookup.
- **Tag attach and detach are idempotent.** `ON CONFLICT DO NOTHING` on attach, `DELETE` returning zero rows on detach — both return `204` with no error. `ticket.tagged` and `ticket.untagged` fire only on an actual change; a no-op writes no event.

### Watchers

- Watcher addition and removal are idempotent.
- The ticket assignee is automatically added as a watcher.
- Agents who comment on a ticket are automatically added as watchers.

### Timeline

- **The ticket timeline is append-only and cursor-paginated oldest-first.** `GET /v1/tickets/{number}/events` reads `ticket_events` joined to the ticket by `(org_id, number)`. Order is `created_at ASC, id ASC` — a timeline reads chronologically, unlike the ticket list. Cursor is opaque, limit defaults to 50 and caps at 100. No code path issues `UPDATE` or `DELETE` on `ticket_events`; the table is an append-only log.
- **Event types and their `data` shapes.** The client switches on `event_type`; each type carries a different JSON object in `data`:

**Audit:**

- The audit helper inserts a row inside a caller's transaction with the
  correct actor type, actor id, action, and entity.

| `event_type` | `data` fields |
|---|---|
| `ticket.created` | `number`, `subject`, `priority` |
| `ticket.status_changed` | `old`, `new` |
| `ticket.assigned` | `old_assignee_id`, `new_assignee_id`, `old_team_id`, `new_team_id`, `auto` (round-robin only) |
| `ticket.tagged` | `tag` |
| `ticket.untagged` | `tag` |
| `comment.added` | `comment_id`, `is_internal` |
| `ticket.deleted` | — |

### Merge

- **Merge closes the source and points it at the target.** `POST /v1/tickets/{number}/merge` with `{into_number}` runs one transaction that locks both rows by id ascending (`FOR UPDATE`, deadlock-safe), checks the rules (same org, different tickets, target not closed or already merged, same requester), sets `source.merged_into_id = target.id`, closes the source, adds a system comment on both tickets, and writes `ticket.merged` on the source. A second concurrent merge of the same pair sees the source already merged and returns `409`.
- **Merged tickets are read-only.** Every mutation — update, assign, comment, delete, tag attach/detach — selects `merged_into_id` under `FOR UPDATE` and returns `409` if it is set. The check is in the repository, next to the lock, so it cannot be bypassed by a caller that skips the service.

### Macros

- **Macros store validated JSON actions and apply through the service.** `macros.body` holds `{"set_status", "set_priority", "add_tags", "comment"}`, validated at create/update (enum values, non-empty tags, 20k comment cap). Applying a macro calls the same service methods as the normal endpoints — `UpdateTicket`, `AttachTag`, `CreateComment` — so the state machine, events, and authorization apply. A macro that sets an illegal transition fails with `422`, exactly like `PATCH /v1/tickets/{number}` would.
- **Macro application is ordered, not atomic.** Actions run in order: status, priority, tags, comment. Status first means an illegal transition fails before anything is written. If a later action fails (e.g. a tag does not exist), earlier actions stay applied. This is deliberate and documented.
- **Macro mutations are admin/owner; GET and apply are any member.** Creating, updating, and deleting a macro require role owner or admin. Listing and applying a macro are open to any org member.

### Pagination

Ticket listing uses keyset pagination.

Pass `?limit=N&cursor=<opaque>` to the endpoint.

| Parameter | Description |
|:--|:--|
| `limit` | Page size. Maximum value: `100`. |
| `cursor` | Opaque continuation token returned by the previous response's `next_cursor`. |
| `next_cursor` | Returned by the server. Its absence indicates the last page. |

The ordering is:

```sql
ORDER BY created_at DESC, id DESC
```

The cursor is opaque and should never be parsed by clients.

---

## Logging

The API writes structured JSON to stdout using `log/slog`, at the level set by
`LOG_LEVEL`. One line is emitted per request and carries the same request ID
returned to the client in `X-Request-ID`:

```json
{"time":"2026-11-03T12:00:00.000Z","level":"INFO","msg":"request","method":"GET","route":"/v1/tickets/{number}","status":200,"duration_ms":3.41,"bytes":418,"request_id":"0f9b…","org_id":"…","user_id":"…"}
```

- `route` is the chi route pattern, never the raw path, so ticket numbers,
  user IDs and query strings do not leak into logs.
- The logger stored in the request context is enriched with `request_id`, so
  service-level log lines carry the same ID as the access line.
- `org_id` and `user_id` are added once a request is authenticated. API-key
  requests log `api_key_id` instead of `user_id`.
- 5xx responses log at `ERROR`; everything else logs at `INFO`.
- Request bodies, query strings, `Authorization` headers, passwords, tokens
  and token hashes are never logged.
- A panic is recovered, logged at `ERROR` with its stack, returned as the
  standard error envelope, and does not stop the server.

---

## Timeouts

The HTTP server is configured with four hard limits:

| Limit | Default | Protects against |
|:--|:--|:--|
| `ReadHeaderTimeout` | `5s` | Slow header writes (Slowloris) |
| `ReadTimeout` | `15s` | Slow request bodies |
| `WriteTimeout` | `30s` | Handlers that ignore cancellation |
| `IdleTimeout` | `60s` | Idle keep-alive connections |

Each request also gets a `REQUEST_TIMEOUT` deadline (default `10s`). When it
expires:

- the request context is cancelled, so PostgreSQL queries and other
  context-aware work stop;
- deadline errors are mapped to `503` with code `request_timeout` in the
  standard error envelope;
- a handler that returns after the deadline without writing a response also
  gets the `503` envelope.

`REQUEST_TIMEOUT` should stay below `SERVER_WRITE_TIMEOUT`. The server write
timeout remains the hard stop for a handler that ignores context cancellation.

---

## Graceful Shutdown

On `SIGTERM` or `SIGINT` the server:

1. stops accepting new connections;
2. waits up to `SHUTDOWN_TIMEOUT` (default `15s`) for in-flight requests;
3. closes the database pool;
4. exits with status `0`.

If the deadline expires, remaining connections are closed and the process
exits non-zero. Keep `SHUTDOWN_TIMEOUT` above `REQUEST_TIMEOUT` so active
requests get a chance to finish cancelling cleanly.

---

## Twelve-Factor Alignment

The table records the current posture. `Partial` marks a deliberate gap that
is listed below it, not a hidden requirement.

| Factor | How Gregport applies it | Status |
|:--|:--|:--:|
| **1. Codebase** | One Git repository with two deployables: `cmd/api` serves HTTP and `cmd/worker` runs background jobs. They share internal packages rather than copy-pasted services. | Yes |
| **2. Dependencies** | Every Go dependency is pinned in `go.mod`/`go.sum`. The runtime image contains only the compiled binary on Alpine; nothing is installed at runtime. | Yes |
| **3. Config** | All settings come from environment variables. `config.Load` and `config.LoadWorker` validate them before startup, secrets have no defaults, `.env` is local-only, and Compose injects values at start. | Yes |
| **4. Backing services** | PostgreSQL is attached through `DB_HOST`, `DB_PORT`, `DB_USER`, `DB_PASSWORD` and `DB_NAME`. Redis is attached through `REDIS_ADDR`, `REDIS_PASSWORD` and `REDIS_DB` for caching, rate limiting, idempotency and the asynq queue. Both endpoints are built at runtime, so they can change without code changes. | Yes |
| **5. Build, release, run** | A multi-stage `Dockerfile` builds a static binary. The image is the immutable release artifact and configuration is supplied when the container starts. CI builds the image; registry publishing is not automated yet. | Yes |
| **6. Processes** | The API is stateless: access tokens are JWTs, refresh tokens are stored hashed in PostgreSQL, and no sessions live in process memory. The worker keeps no durable state either; `ticket_events` is the outbox and Redis holds only queue/cache state. | Yes |
| **7. Port binding** | The API binds `:APP_PORT` (`8080` by default) and the worker binds `:WORKER_PORT` (`8081`) for health probes. Compose publishes the ports; no external web server is required. | Yes |
| **8. Concurrency** | Scale horizontally by running more API or worker containers. Each process has its own pgx pool; workers coordinate claims through PostgreSQL (`FOR UPDATE SKIP LOCKED`) and asynq, with no sticky sessions. The worker also splits I/O and CPU queues across two servers. | Yes |
| **9. Disposability** | Configuration fails fast, startup is quick, `SIGTERM`/`SIGINT` drain in-flight requests and jobs, requests have deadlines, and Compose allows `20s` for the API and `40s` for the worker. Unfinished events stay in the outbox and are reclaimed. | Yes |
| **10. Dev/prod parity** | Local Compose and CI use the same images, Go version, `postgres:16-alpine`, `redis:7-alpine` and migration SQL. A production environment and automated release are not set up yet. | Partial |
| **11. Logs** | Both binaries write JSON to stdout with request IDs and route patterns; neither writes log files. Docker captures the stream, and secrets and raw paths are never logged. | Yes |
| **12. Admin processes** | Migrations are one-off SQL executed by the PostgreSQL init step, and admin queries use `psql`; they do not run inside the API process. The worker is a separate deployable process. A standalone migration command is still not implemented. | Partial |

**Remaining gaps**

- No production environment or automated registry/release pipeline yet.
- Migrations run only when the Compose database volume is first initialized;
  there is no standalone migration command for existing databases.
- Logs are stdout-only; there is no bundled aggregation or retention layer.
- Webhooks are schema-defined but have no producer or consumer yet.

---

## Testing

The project includes integration tests, authentication unit tests, logging and middleware tests, and security-focused token tests.

### Running Tests

Run the authentication package tests:

```bash
go test ./internal/auth/ -v
```

Run the entire test suite:

```bash
go test -p 1 ./... -race -count=1
```

Ticket tests are integration tests and require PostgreSQL. CI runs the same
command against a fresh, migrated PostgreSQL 16 service. `-p 1` is deliberate:
every integration package shares one database, and the outbox dispatcher claims
rows globally, so running packages in parallel would let one package consume
another's pending events.

### Integration Tests

These tests use a real PostgreSQL database.

**Authentication**

- Signup transaction rollback.
- Refresh-token rotation and reuse detection.

**Tickets**

- Concurrent ticket creation: 20 concurrent creates produce ticket numbers `1–20` without gaps or duplicates.
- Failed ticket creation does not leave a gap in per-organization numbering.
- Tenant isolation: every ticket endpoint returns `404` when organization B accesses organization A's ticket.
- Update concurrency: two simultaneous updates result in one successful update and one `412 Precondition Failed`.
- Version mismatch rejection.
- Invalid state transition rejection.
- Valid state transition and event creation.
- Transaction rollback when event creation fails, verified using a forced database trigger.
- Manual assignment to an agent.
- Assignment to a non-member returns `422`.
- Team-only assignment.
- Cross-organization team assignment returns `404`.
- Team creation.
- Duplicate team creation returns `409`.
- Agent team-management attempt returns `403`.
- Adding a cross-organization member returns `422`.
- Tags: create lowercases and trims the name; duplicate per org returns `409`; attach and detach are idempotent (one event per actual change); attaching a nonexistent tag returns `404`; attaching a tag to another org's ticket returns `404`.

**Watchers**
- add is idempotent; adding a non-member returns `422`;
remove is idempotent; removing a watcher from another org's ticket returns `404`;
an agent who comments is auto-added as a watcher.

**Timeline**

- events appear in order (created, assigned, tagged, comment, status);
cursor pagination returns every event exactly once; another org's ticket returns `404`.

**Merge**
- happy path closes the source and writes one `ticket.merged` event;
merge into self returns `422`; merging into another org's ticket returns `404`;
two concurrent merges of the same pair yield exactly one success;
a comment on a merged source returns `409`.

**Macros**
- create validates status/priority enums and rejects unknown fields;
duplicate name returns `409`; applying a macro sets status, attaches tags, 
and adds a comment; an illegal status transition in a macro returns `422` 
(state machine applies); another org's macro returns `404`.

**Audit**

- `member.role_changed` and `member.removed` write audit rows; the
  invitation and API key audit rows are asserted in their own tests. Every
  state-changing action in feat 5 commits its audit row inside the same
  transaction as the mutation, so a crash cannot leave a change unaudited.

**Invitations**

- owner invites agent (201, raw token returned once, token
  hashed in DB, audit row written); admin inviting admin returns 403;
  owner inviting owner returns 403; accept creates a new user + membership
  with an audit row; accept by an existing user with a mismatched email
  returns 403; expired and already-used tokens return 422; a duplicate
  membership returns 409.

**Members**

- any member can list the roster; owner promotes agent to admin;
  admin promoting to admin returns 403 (grant table); granting owner returns
  403; removing the last owner returns 422; two concurrent removals of the
  last two owners leave exactly one owner; removing a member revokes their
  refresh tokens for this org.

**API Key**
- A valid key returns a principal with `Kind: api_key`, the
  key's id as `ActorID`, and role `agent`; revoked, expired, wrong-secret,
  and unknown-prefix keys all return 401; `last_used_at` is written at most
  once per minute per key; an API key can create a ticket and cannot create
  an API key.

### Unit Tests

Authentication unit tests use a fake `Repo` implementation and do not require a database.

- Signup validation and normalization.
- Identical responses for unknown email and incorrect password, using timing-safe dummy bcrypt verification.
- Organization selection:
  - Single organization.
  - Multiple organizations without a slug.
  - Multiple organizations with a slug.
- Refresh-token reuse detection and token-family revocation.
- Unknown tokens do not trigger reuse detection.
- Logout revokes refresh tokens by hash.

The authentication service depends on the `auth.Repo` interface rather than the concrete `*Repository`. Production injects the real repository, while tests inject a fake implementation.

Ticket tests are integration tests and require PostgreSQL.

- **Policy grant table** (`policy_test.go`): every `(caller kind, caller
  role, target role)` triple — owner invites admin/agent, admin invites
  agent, nobody invites owner, API keys cannot invite or manage anything.
  The `Kind` gate is checked before the role in every management function.

### Token Tests

These tests perform real JWT signing and verification without a database.

- Access-token round trip.
- Expiry rejection.
- Tamper detection.
- `alg: none` rejection.
- Wrong-secret rejection.
- Refresh-token uniqueness and hashing.

They verify actual security behavior, including algorithm pinning, required expiration, and tamper detection.

### Logging and Middleware Tests

These tests do not require a database.

- Log level parsing, including rejection of unknown levels, and JSON output
  shape.
- Request IDs are generated when absent, echoed in `X-Request-ID`, and appear
  on every access-log line.
- Access-log fields: method, route pattern, status, duration, bytes, and the
  authenticated principal (`user_id` or `api_key_id`, plus `org_id`).
- Raw paths, query strings, request bodies, `Authorization` headers and known
  secret values never appear in log output.
- 5xx responses log at `ERROR`; unmatched routes log the `unmatched`
  placeholder rather than the requested path.
- Panic recovery writes the standard envelope, logs the stack with the panic
  value, and the server keeps serving; `http.ErrAbortHandler` is re-panicked.
- The request-info holder observes the principal attached by authentication
  under concurrent access (verified with `-race`).

### Error Envelope Tests

These tests do not require a database.

- One envelope shape with `code`, `message`, `details` (always `[]`) and
  `request_id`; the request ID matches `X-Request-ID`.
- Wrapped domain errors keep their status and code, while unknown errors
  return a generic `500` and log the real cause without sending it to the
  client.
- Shared domain sentinels are not mutated between requests.
- Every ticket and auth sentinel is asserted against its status and code and
  still matches through `errors.Is` when wrapped.
- JSON decoding maps malformed input, unknown fields, wrong types, empty
  bodies, extra objects and oversized bodies to safe codes without leaking
  decoder text.
- Panic recovery returns the envelope with `details: []` and the request ID.

### Configuration Tests

These tests do not require a database.

- Every setting has its default asserted, including token lifetimes, server
  timeouts and the shutdown timeout.
- Environment overrides replace each default.
- Missing `DB_PASSWORD`, short `JWT_SECRET`, invalid `LOG_LEVEL`, out-of-range
  ports, non-positive durations and invalid duration syntax all fail.
- `REQUEST_TIMEOUT=0` is accepted and disables the per-request deadline.
- Request/write ordering and shutdown/request ordering are enforced.
- Multiple problems are joined into one error instead of failing one at a time.
- The DSN escapes reserved characters in usernames and passwords, keeps the
  original values after URL parsing, and formats IPv6 hosts with brackets.

### Queue and Worker Tests

The queue tests use `miniredis`, so they are fast and need no live Redis.
The dispatcher tests are PostgreSQL integration tests.

- Queue weights are `critical: 6`, `default: 3`, `low: 1`, and the CPU queue is
  isolated on its own server.
- A saturated CPU pool does not starve the I/O pool: with every CPU worker
  blocked, I/O tasks still complete on the separate server. The CPU server
  also enforces `WORKER_CPU_CONCURRENCY` during a burst.
- `BenchmarkQueueRoundTrip` measures enqueue-to-completion latency for a no-op
  task on each queue, giving the queue-overhead baseline used for worker
  sizing.
- Task marshaling, validation of incomplete jobs, and `queue.Permanent`
  wrapping `asynq.SkipRetry`.
- Backoff is exactly 5s, 10s, 20s … capped at one hour, and two hundred samples
  per attempt stay inside the ±20% jitter band while still varying.
- Inspector statistics report pending/size per queue and skip idle queues.
- Dead-letter lifecycle: archive, list with payload/retry metadata, requeue,
  archive again, delete, and verify the queue is empty.
- Dead-letter pagination returns every task exactly once and clamps page zero
  and oversized page sizes.
- Inspector input validation and cancelled-context propagation for every
  operation.
- Queue snapshot logging emits `INFO` normally, `WARN` when `archived > 0`,
  logs inspector errors and keeps ticking, stops on cancellation, and is fully
  disabled at `WORKER_QUEUE_STATS_INTERVAL=0`.
- Outbox dispatcher: one event becomes one deterministic job and is marked
  dispatched; two dispatchers splitting 150 events produce 150 distinct jobs
  with none left undispatched; an enqueue failure rolls the whole batch back; a
  partial failure leaves every event undispatched for the retry; unknown event
  types are marked dispatched without jobs; `Run` dispatches on its interval
  and stops on cancellation.
- The full ticket-event pipeline runs end to end against PostgreSQL and a real
  asynq server on in-process Redis: ticket creation applies SLA due dates and
  writes the outbox row, the consumer map builds the deterministic email job,
  queue routing reaches the email handler, and the delivery is persisted as
  `sent`. The test replays the task twice and proves only one email is sent,
  then drives the breach and auto-close checkers and verifies their events use
  the same path (`sla.breached` emails; `ticket.closed` has no consumer). It
  also proves a solved status change emits a `ticket_solved` email with a
  verifiable rating token, while a non-solved transition emits nothing.

### SLA, Email and Housekeeping Tests

These tests mix pure unit checks with PostgreSQL integration tests.

- The SLA calculator is pure and measured against wall-clock targets: first
  response and resolution deadlines, priority policy selection, and due-date
  recomputation from the original `created_at` when priority changes.
- The breach checker marks first-response and resolution breaches once, records
  one `sla.breached` event per breach, skips responded/resolved and soft-deleted
  tickets, and stays idempotent with concurrent runners.
- Auto-close only touches solved tickets past the configured window, stamps
  `closed_at`, records one `ticket.closed` event, and is idempotent under
  concurrent runners.
- Email planning covers every event type and recipient rule (requester, agent,
  support inbox, internal-comment suppression). Delivery tests cover the
  unique-index idempotency, success/failed/dead transitions, retry scheduling,
  the attempt cap, circuit-breaker tripping and reset, SMTP failure handling and
  permanent validation failures.
- Customer email tests cover the `[#1042]` subject, `Reply-To` from
  `settings.support_email`, multipart plain-text + HTML bodies, HTML escaping of
  comment bodies and subjects, CR/LF header-injection sanitization, the
  agent-public-comment-only rule, and replay producing neither a second
  delivery nor a second send.
- Housekeeping purges each retained row type independently, honours batch
  limits, skips disabled windows, and continues other purges when one fails.
  Foreign-key safety is covered by the ticket-event purge, which skips events
  still referenced by an email delivery.

### Notification Tests

The notification tests mix unit-style recipient/preference checks with
PostgreSQL integration tests.

- Recipients are the assignee plus watchers, de-duplicated and excluding the
  acting user; the customer requester never appears as an in-app recipient.
- A notification persists its ticket, comment and event ids plus a stable
  payload, and replaying the same event inserts zero rows thanks to the
  `(event_id, user_id)` dedupe index.
- Preferences default to enabled for the full event-type × channel matrix,
  unknown types/channels and duplicates are rejected, and only disabled
  exceptions are stored.
- Listing is newest-first with keyset cursor pagination, the `unread` filter
  excludes read rows, and every read/mark operation is scoped to the caller's
  `org_id` and `user_id`.
- Mark-read is idempotent; another user's or another org's notification is a
  permanent 404 rather than a silent success.

### Satisfaction Rating Tests

The rating tests combine token unit tests with PostgreSQL integration tests.

- Signing and verification round-trips the ticket and customer ids; tampered
  payloads, signatures, extra segments and tokens signed with another secret
  are all rejected, and an expired token returns `ErrExpiredToken`.
- The service rejects scores outside `1`–`5`, oversized comments and tampered or
  expired tokens before any write, and maps a foreign or missing ticket to
  `ErrRatingNotAvailable` so ticket ids cannot be probed.
- `UNIQUE (ticket_id)` is verified against PostgreSQL: the second submission
  returns `ErrAlreadyRated` and leaves the original score unchanged.
- The handler returns `201` with no identifiers beyond the ticket number, `409
  already_rated`, `422 invalid_token` / `expired_token` / `invalid_score` /
  `invalid_comment`, and a malformed body returns `400`.
- Dashboard and repository tests verify the org-scoped average and count,
  including two tenants with separate ratings and a second tenant seeing `0`
  until it has its own.
- The outbox pipeline test shows a solved status change enqueues exactly one
  email consumer, the sent `ticket_solved` delivery carries a verifiable token,
  and a non-solved transition enqueues nothing.

### Timeout Tests

These tests do not require a database.

- Server construction applies the configured address, handler and all four
  server timeout values.
- A fast handler keeps its status, headers and body, and receives a
  deadline-bearing context.
- A handler that overruns receives a cancelled context and the client gets
  `503` with code `request_timeout` in the standard envelope, including the
  request ID.
- A real `httptest.Server` with the full middleware chain returns the same
  envelope, the matched route pattern in the access log, and a matching
  `X-Request-ID`.
- Wrapped deadline errors map to the same `503` envelope, while deliberate
  `503` domain errors keep their own code.
- Panics still reach recovery and return the `500` envelope when the timeout
  middleware is installed.

### Health Check Tests

These tests do not require a database.

- Liveness returns `200` and JSON without touching a database.
- Readiness pings with a deadline and returns `200` when healthy.
- A failed ping returns `503` with `{"status":"unready"}`, hides the database
  error, and logs it at `WARN` with the request ID.
- A real `pgxpool.Pool` returns `200` while healthy and `503` after it is
  closed; the test skips when PostgreSQL is not reachable.

### Graceful Shutdown Tests

- Cancelling the server context stops the listener from accepting new
  connections while an in-flight request is still running.
- The in-flight request finishes with its original response before
  `runServer` returns.
- A handler that overruns `SHUTDOWN_TIMEOUT` causes a shutdown error and the
  remaining connections are closed.
- Serve errors are propagated instead of being mistaken for a clean shutdown.

### Container and Compose Checks

- `docker compose config --quiet` validates the rendered stack.
- Missing `DB_PASSWORD` or `JWT_SECRET` fails interpolation before any
  container starts.
- `APP_PORT` overrides appear in both the API environment and the published
  port; the port map uses the same value on both sides.
- `api` waits for the `db` health check, and its own health check uses the
  Alpine `wget` command against `/healthz`.
- A fresh database initialized from the three `*.up.sql` files in order has
  all 27 tables, the ticket `version` column and `audit_logs`.
- The Docker-built binary starts against that fresh database, passes
  `/healthz` and `/readyz`, serves signup, login and ticket creation, and
  exits `0` on `SIGTERM`.
- The Alpine health-check command returns `0` for a healthy response and
  non-zero for an error response.

### Ticket State Machine Tests

The ticket transition logic is implemented through `CanTransition(from, to)` and tested using a table-driven approach.

- Map-of-maps transition table.
- `closed` is a terminal state.
- 27 table-driven sub-tests, written before implementation using Test-Driven Development (TDD).

### Round-robin Assignment

20 concurrent auto-assigns to one team spread across all 3 agents without errors; an empty team returns `422`.

### Test Summary

Run `go test -p 1 ./... -race -count=1` and read the output to see the current test results.

The authentication cycle has also been verified end to end using `curl`:

1. Login returns an access and refresh token pair.
2. `/v1/me` accepts a valid access token and rejects unauthenticated requests.
3. Refresh rotates the refresh token.
4. Replaying the previous refresh token returns `401`.
5. Logout returns `204`.
6. Incorrect passwords and unknown emails produce identical `401` responses.

---

## Continuous Integration

`.github/workflows/ci.yml` runs on pushes to `main`, pull requests, and manual
dispatch. Every run:

1. Starts PostgreSQL 16 as a service container and applies all
   `migrations/*.up.sql` files in order.
2. Checks that `go.mod` and `go.sum` are tidy, then runs `go vet ./...` and
   `go test -p 1 ./... -race -count=1` against that real database.
3. Validates the rendered Compose stack with `docker compose config --quiet`.
4. Builds the production image.
5. Checks that the image runs as UID/GID `10001`.
6. Checks that invalid startup configuration exits non-zero.
7. Starts the container against PostgreSQL, waits for `/readyz`, checks
   `/healthz`, and verifies that `SIGTERM` exits `0`.

The same checks can be run locally with the commands in
[Testing](#testing) and [Docker Compose Stack](#docker-compose-stack).

---

## Current Development Direction

The worker, queue, SLA engine, notifications, email and rating blocks are
complete. The API writes domain events to the `ticket_events` outbox in the
same transaction as the change; `cmd/worker` claims them, enqueues
deterministic asynq jobs and runs the consumers. Email is the first consumer:
ticket creation, comments, assignment, SLA breaches and solved transitions
produce idempotent deliveries with retries, a circuit breaker and SMTP
transport; solved email carries the signed rating link.

SLA automation is complete as well: policies set first-response and
resolution deadlines on ticket create and priority change, a scheduler records
breaches, and another scheduler auto-closes solved tickets after the configured
window. Housekeeping prunes expired idempotency keys, dispatched events, old
audit rows and delivered email rows.

Production basics remains complete: structured JSON logging with request IDs,
the unified error envelope, server/request timeouts, liveness/readiness checks,
graceful shutdown, fail-fast configuration validation, multi-stage images for
both binaries, the Compose stack, CI and the twelve-factor alignment table.

Remaining work: webhook delivery (schema-defined, no producer or consumer yet),
production deployment and release automation, and standalone migration tooling
for existing databases.
