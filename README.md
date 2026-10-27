# gregport

A multi-tenant support ticketing SaaS — organizations, users, memberships,
tickets, comments, SLA policies, notifications, and webhooks.

---

## Status

<div align="center">

| **Area** | **State** |
|:--|:--|
| Auth (signup, login, refresh, logout, `/v1/me`) | Complete |
| JWT · refresh rotation · reuse detection | Complete + tested |
| HTTP layer (login, refresh, logout, `/v1/me`, `Authenticate`, `RequireRole`) | Next slice |
| Tickets domain | Complete — create, list, get, update, delete, comments; state machine, keyset pagination, optimistic concurrency |
| Comments · SLA · notifications · webhooks · API keys · invitations | Schema defined, not exposed |

</div>

The auth cycle is complete end to end: a user can sign up, log in, call a
protected route, rotate their refresh token, and log out. Every protected
request carries a verified `user_id`, `org_id`, and `role` in its context.

JWT access tokens, hashed refresh tokens, refresh-token rotation with reuse
detection, and the Login / Refresh / Logout services are implemented and
tested. The HTTP layer lands in the next slice.

Tickets: create (single transaction across counter, customer, ticket, comment,
event), list (keyset pagination), get/update (optimistic concurrency via
`If-Match` on `version`), soft delete (admin/owner), and comments are
implemented and tested at the repository layer. The status state machine is
table-driven. HTTP-level tenant and concurrency tests land next.

---

## Stack

<table>
<tr><td><b>Language</b></td><td>Go 1.27</td></tr>
<tr><td><b>Router</b></td><td><a href="https://github.com/go-chi/chi">chi</a></td></tr>
<tr><td><b>Database</b></td><td>PostgreSQL 16 (Docker Compose)</td></tr>
<tr><td><b>Driver</b></td><td><a href="https://github.com/jackc/pgx">pgx/v5</a> — pool + transactions</td></tr>
<tr><td><b>Auth</b></td><td><a href="https://go.dev">bcrypt</a> · <a href="https://github.com/golang-jwt/jwt">golang-jwt/jwt/v5</a></td></tr>
<tr><td><b>IDs</b></td><td><a href="https://github.com/google/uuid">google/uuid</a></td></tr>
</table>

---

## Project layout

<details>
<summary><code>cmd/api/</code></summary>

- `main.go` — entry point

</details>

<details>
<summary><code>internal/auth/</code> — signup + tokens</summary>

- `models.go`
- `repo.go`
- `service.go`
- `handler.go`
- `errors.go`
- `tokens.go`
- `repository_tokens_test.go`

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
- `testhelpers_test.go`

</details>
<details>
<summary><code>internal/middleware/</code></summary>

- `requestid.go`
- `recovery.go`

</details>

<details>
<summary><code>internal/platform/</code> — shared infrastructure</summary>

- `config/` — Config + Load (env)
- `database/` — pgxpool + WithTx
- `httpx/` — DecodeJSON, WriteJSON, WriteError, APIError
- `requestctx/` — Principal in context

</details>

<details>
<summary><code>migrations/</code></summary>

- SQL schema

</details>

### Layering

Each feature is a package under `internal/` containing its own models,
repository, service, and handler. Dependencies point one way:

<div align="center">

`handler` → `service` → `repository` → `database`

</div>

The service layer is format-agnostic — it never touches JSON or SQL. The
repository is the only place that speaks SQL. The handler is the only place
that speaks JSON.

---

## Configuration

Copy `.env.example` to `.env` and fill in real values. The app refuses to
start if required values are missing.

<table>
<tr><th align="left">Variable</th><th align="left">Required</th><th align="left">Default</th><th align="left">Notes</th></tr>
<tr><td><code>DB_HOST</code></td><td>no</td><td><code>localhost</code></td><td></td></tr>
<tr><td><code>DB_PORT</code></td><td>no</td><td><code>5432</code></td><td></td></tr>
<tr><td><code>DB_USER</code></td><td>no</td><td><code>postgres</code></td><td></td></tr>
<tr><td><code>DB_PASSWORD</code></td><td>yes</td><td>—</td><td></td></tr>
<tr><td><code>DB_NAME</code></td><td>no</td><td><code>gregport</code></td><td></td></tr>
<tr><td><code>APP_PORT</code></td><td>no</td><td><code>8080</code></td><td></td></tr>
<tr><td><code>JWT_SECRET</code></td><td><b>yes</b></td><td>—</td><td>min 32 bytes — <code>openssl rand -base64 32</code></td></tr>
<tr><td><code>ACCESS_TOKEN_TTL</code></td><td>no</td><td><code>15m</code></td><td>Go duration</td></tr>
<tr><td><code>REFRESH_TOKEN_TTL</code></td><td>no</td><td><code>720h</code></td><td>Go duration (30 days)</td></tr>
</table>

`JWT_SECRET` has no default and the app will `log.Fatal` without it. Never
commit it, never log it, and don't reuse it across projects.

---

## Running locally

```bash
cp .env.example .env      # fill in real values, including JWT_SECRET
docker compose up -d --wait
go run cmd/api/main.go
```

The app listens on `:8080` (override with `APP_PORT`).

<div align="center">

**Health check**

</div>

```bash
curl localhost:8080/healthz
# {"status":"ok"}          with DB up
# {"status":"unhealthy"}   with DB down
```

---

## Database schema

![ERD](docs/ERD.svg)

---

## Testing

```bash
go test ./internal/auth/ -v
```

<details open>
<summary><b>Integration</b> — real Postgres</summary>

- Signup transaction rollback
- Refresh-token rotation and reuse
- (Tickets) 20 concurrent creates produce numbers 1–20 with no gaps or
  duplicates; a failed create leaves no gap in the per-org numbering

</details>

<details>
<summary><b>Unit</b> — fake <code>Repo</code>, no database</summary>

- Signup validation and normalization
- Identical errors for unknown email and wrong password (timing-safe dummy bcrypt)
- Org selection (single / multi without slug / multi with slug)
- Reuse detection triggering family revocation
- Unknown tokens not triggering it
- Logout revoking by hash

The service tests exist because `Service` depends on the `auth.Repo`
interface, not the concrete `*Repository`. Production injects the real repo;
tests inject a fake. That's what makes the service testable without a
database.

</details>

<details>
<summary><b>Tokens</b> — real JWT signing, no database</summary>

- Access-token round trip
- Expiry rejection
- Tamper rejection
- `alg: none` rejection
- Wrong-secret rejection
- Refresh-token uniqueness and hashing

The token tests run real JWT signing and verification — no mocks. They prove
the security properties (algorithm pinning, expiry required, tamper detection)
actually hold, not just that the code was written.

</details>

<details>
<summary><b>Tickets</b> — <code>CanTransition(from, to)</code>, table-driven</summary>

- Map-of-maps transition table
- `closed` is terminal
- 27 table-driven sub-tests, written before the implementation (TDD)

</details>

<div align="center">

All 32 tests pass without a database except the two integration files
(`repository_test.go`, `repository_tokens_test.go`), which need Postgres.

</div>

The auth cycle was also verified end to end with `curl`: login returns a
token pair, `/v1/me` accepts the access token and rejects requests without
one, refresh rotates and replaying the old token returns 401, logout returns
204, and wrong-password / unknown-email return **identical** 401 responses.

---

## Pagination

Ticket listing uses **keyset pagination**. Pass `?limit=N&cursor=<opaque>`.
The response includes `next_cursor`; absent means last page.

<div align="center">

<table>
<tr><th align="left">Parameter</th><th align="left">Meaning</th></tr>
<tr><td><code>limit</code></td><td>Page size. Caps at <b>100</b>.</td></tr>
<tr><td><code>cursor</code></td><td>Opaque continuation token from the previous <code>next_cursor</code>. Do not parse it.</td></tr>
<tr><td><code>next_cursor</code></td><td>Returned by the server. <b>Absent</b> = last page.</td></tr>
</table>

</div>

Order is `created_at DESC, id DESC`.

## Rules
- **Tickets use per-org numbers, not UUIDs, in URLs.** `(org_id, number)` is
  the lookup key. A ticket in another org returns 404, never 403 — the API
  never confirms existence in someone else's tenant.
- **PATCH requires `If-Match`.** Missing header → 428, stale version → 412.
  Every ticket update bumps `version`, so two agents editing the same ticket
  fail visibly instead of silently overwriting.
- **Ticket list is keyset-paginated.** `?limit=N&cursor=<opaque>`,
  `next_cursor` in the response, absent means last page. Cursor is opaque.
  Order is `created_at DESC, id DESC`. Limit caps at 100.
