# gregport

A multi-tenant support ticketing SaaS — organizations, users, memberships,
tickets, comments, SLA policies, notifications, and webhooks.

## Status

**Signup feature complete. Auth token layer in place.** The database schema is
applied, the HTTP boundary layer is in place, and `POST /v1/auth/signup`
creates an organization, a user, and an owner membership in a single
transaction.

JWT access tokens and hashed refresh tokens are implemented and tested, but
not yet wired to HTTP endpoints — login, refresh, logout, and the auth
middleware land in the next slice.

Everything else in the schema (tickets, comments, SLA, notifications, webhooks,
API keys, invitations) is defined but not yet exposed.

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
                             errors, tokens, tests
  middleware/                RequestID, Recovery
  platform/
    config/                  Config + Load (env)
    database/                pgxpool + WithTx
    httpx/                   DecodeJSON, WriteJSON, WriteError, APIError
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

## Testing

```bash
go test ./internal/auth/ -v
```

Three kinds of tests:

- **Integration** (`internal/auth/repository_test.go`) — run against a real
  Postgres. Prove the signup transaction rolls back: a duplicate-email signup
  leaves no orphan organization row.
- **Unit** (`internal/auth/service_test.go`) — use a fake `Repo`, no database.
  Prove validation, email normalization, and error passthrough.
- **Tokens** (`internal/auth/tokens_test.go`) — use a test secret, no database.
  Prove the access-token round trip, expiry rejection, tamper rejection,
  `alg: none` rejection, wrong-secret rejection, and refresh-token uniqueness
  and hashing.

The service tests exist because `Service` depends on the `auth.Repo` interface,
not the concrete `*Repository`. Production injects the real repo; tests inject
a fake. That's what makes the service testable without a database.

The token tests run real JWT signing and verification — no mocks. They prove
the security properties (algorithm pinning, expiry required, tamper detection)
actually hold, not just that the code was written.
