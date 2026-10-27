# gregport

A multi-tenant support ticketing SaaS — organizations, users, memberships,
tickets, comments, SLA policies, notifications, and webhooks.

## Status

**Signup, login, refresh, logout, and `/v1/me` are live.** The auth cycle is
complete: a user can sign up, log in, call a protected route, rotate their
refresh token, and log out. Every protected request carries a verified
`user_id`, `org_id`, and `role` in its context. The ticket domain is next.

JWT access tokens, hashed refresh tokens, refresh-token rotation with reuse
detection, and the Login/Refresh/Logout services are implemented and tested.
The HTTP layer — login, refresh, logout, `/v1/me`, and the `Authenticate` /
`RequireRole` middleware — lands in the next slice.

Everything else in the schema (comments, SLA, notifications, webhooks,
API keys, invitations) is defined but not yet exposed.

tickets models and handlers are initiated.
new migrations added, tickets table altered and now have a version column

tickets: status state machine with table-driven test added.

## Stack

- Go 1.27
- [chi](https://github.com) — router
- [pgx/v5](https://github.com) — PostgreSQL driver and pool
- PostgreSQL 16 (via Docker Compose)
- [bcrypt](https://go.dev) — password hashing
- [golang-jwt/jwt/v5](https://github.com) — access tokens
- [google/uuid](https://github.com) — UUIDs

## Layout

cmd/api/                     entry point (main.go)
internal/
  auth/                      signup + tokens: models, repo, service, handler,
                             errors, tokens, tests (incl. repository_tokens_test.go)
  middleware/                RequestID, Recovery
  platform/
    config/                  Config + Load (env)
    database/                pgxpool + WithTx
    httpx/                   DecodeJSON, WriteJSON, WriteError, APIError
    requestctx/              Principal in context
migrations/                  SQL schema

Each feature is a package under `internal/` containing its own models,
repository, service, and handler. Dependencies point one way:
`handler → service → repository → database`. The service layer is
format-agnostic — it never touches JSON or SQL. The repository is the only
place that speaks SQL. The handler is the only place that speaks JSON.

## Configuration

Copy `.env.example` to `.env` and fill in real values. The app refuses to
start if required values are missing.

| Variable            | Required | Default     | Notes                                                 |
|---------------------|----------|-------------|-------------------------------------------------------|
| `DB_HOST`           | no       | `localhost` |                                                       |
| `DB_PORT`           | no       | `5432`      |                                                       |
| `DB_USER`           | no       | `postgres`  |                                                       |
| `DB_PASSWORD`       | yes      | —           |                                                       |
| `DB_NAME`           | no       | `gregport`  |                                                       |
| `APP_PORT`          | no       | `8080`      |                                                       |
| `JWT_SECRET`        | **yes**  | —           | min 32 bytes; generate with `openssl rand -base64 32` |
| `ACCESS_TOKEN_TTL`  | no       | `15m`       | Go duration                                           |
| `REFRESH_TOKEN_TTL` | no       | `720h`      | Go duration (30 days)                                 |

`JWT_SECRET` has no default and the app will `log.Fatal` without it. Never
commit it, never log it, and don't reuse it across projects.

## Running locally

```bash
cp .env.example .env      # fill in real values, including JWT_SECRET
docker compose up -d --wait
go run cmd/api/main.go
```

The app listens on `:8080` (override with `APP_PORT`).

Health check:

```bash
curl localhost:8080/healthz
# {"status":"ok"}          with DB up
# {"status":"unhealthy"}   with DB down
```

## Database Schema

![ERD](docs/ERD.svg)

## Testing

```bash
go test ./internal/auth/ -v
```

tests:

- **Integration** (`internal/auth/repository_test.go`,
  `internal/auth/repository_tokens_test.go`) — run against a real Postgres.
  The signup tests prove the transaction rolls back (a duplicate-email signup
  leaves no orphan organization row). The token tests prove rotation is atomic,
  replaying a spent refresh token fails, reuse detection revokes the whole
  family, soft-deleted orgs block both login and rotation, and expired tokens
  are rejected.
- **Unit** (`internal/auth/service_test.go`) — use a fake `Repo`, no database.
  Prove signup validation and normalization, identical errors for unknown email
  and wrong password (timing-safe dummy bcrypt), org selection (single / multi
  without slug / multi with slug), reuse detection triggering family revocation,
  unknown tokens not triggering it, and logout revoking by hash.
The service tests exist because `Service` depends on the `auth.Repo` interface,
not the concrete `*Repository`. Production injects the real repo; tests inject
a fake. That's what makes the service testable without a database.

- **Tokens** (`internal/auth/tokens_test.go`) — use a test secret, no database.
  Prove the access-token round trip, expiry rejection, tamper rejection,
  `alg: none` rejection, wrong-secret rejection, and refresh-token uniqueness
  and hashing.
The token tests run real JWT signing and verification — no mocks. They prove
the security properties (algorithm pinning, expiry required, tamper detection)
actually hold, not just that the code was written.
- **Tickets** (`internal/tickets/tickets_test.go`) - CanTransition(from, to) via map-of-maps; closed is terminal
27 table-driven sub-tests, written before the implementation (TDD)

All 32 tests pass without a database except the two integration files
(`repository_test.go`, `repository_tokens_test.go`), which need Postgres.

The auth cycle was also verified end to end with `curl`: login returns a
token pair, `/v1/me` accepts the access token and rejects requests without
one, refresh rotates and replaying the old token returns 401, logout returns
204, and wrong-password / unknown-email return **identical** 401 responses.
