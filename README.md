# gregport

A multi-tenant support ticketing SaaS — organizations, users, memberships,
tickets, comments, SLA policies, notifications, and webhooks.

## Status

**complete: signup feat.** The database schema is applied, the HTTP boundary
layer is in place, and `POST /v1/auth/signup` creates an organization, a user,
and an owner membership in a single transaction.

Everything else in the schema (tickets, comments, SLA, notifications, webhooks,
API keys, invitations) is defined but not yet exposed. See [Roadmap](#roadmap).

## Stack

- Go 1.27
- [chi](https://github.com/go-chi/chi) — router
- [pgx/v5](https://github.com/jackc/pgx) — PostgreSQL driver and pool
- PostgreSQL 16 (via Docker Compose)
- [bcrypt](https://pkg.go.dev/golang.org/x/crypto/bcrypt) — password hashing
- [google/uuid](https://github.com/google/uuid) — UUIDs

## Layout
cmd/api/ — entry point (main.go)<br>
internal/auth/ — signup feat: models, repo, service, handler, errors<br>
internal/middleware/ — RequestID, Recovery<br>
internal/platform/config/ — Config + Load (env)<br>
internal/platform/database/ — pgxpool + WithTx<br>
internal/platform/httpx/ — DecodeJSON, WriteJSON, WriteError, APIError<br>
internal/migrations/ — SQL schema


Each feature is a package under `internal/` containing its own models,
repository, service, and handler. Dependencies point one way:
`handler → service → repository → database`. The service layer is
format-agnostic — it never touches JSON or SQL. The repository is the only
place that speaks SQL. The handler is the only place that speaks JSON.

## Running locally

```bash
cp .env.example .env      # fill in real values
docker compose up -d --wait
go run cmd/api/main.go
