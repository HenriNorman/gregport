# OWASP API Security Top 10 (2023) review

Every item below has an owner and an answer. The code references are the
mechanism, not just an intention.

| # | Risk | How Gregport handles it |
|---|------|--------------------------|
| API1 | Broken object level authorization | `org_id` comes only from the authenticated principal; every ticket, comment, attachment, webhook and export query filters on it, and another tenant's object returns 404. Covered by `internal/tickets/tenant_test.go` and sibling tenant suites. |
| API2 | Broken authentication | Short-lived JWT access tokens, rotating single-use refresh tokens with family revocation on reuse, bcrypt cost 12 for new hashes (`BCRYPT_COST`), per-IP login rate limiting plus per-account backoff (`internal/auth/backoff.go`), and a constant-time dummy bcrypt compare for unknown emails. |
| API3 | Broken object property level authorization | Handlers return explicit DTOs (`TicketDTO`, `CommentDTO`, …) and never serialize database rows; role checks gate owner/admin mutations; internal comments require an agent role. |
| API4 | Unrestricted resource consumption | Redis sliding-window rate limits on authenticated, public, login and rating buckets; `MAX_REQUEST_BYTES` body limit; bounded page sizes; request timeouts; pgx pool caps sized against `max_connections`. |
| API5 | Broken function level authorization | `auth.RequireRole("owner", "admin")` is applied per route for destructive and administrative operations, and API-key authentication resolves to the same principal/role model as user tokens. |
| API6 | Unrestricted access to sensitive business flows | Ticket creation, exports, invitations, webhook tests and rating submission are authenticated, rate-limited and audited; exports are limited to owner/admin. |
| API7 | Server side request forgery | Outgoing webhooks pass URL validation and DNS/IP checks that reject loopback, link-local, private and metadata ranges before every send (`internal/webhooks/ssrf.go`); redirects are not followed. |
| API8 | Security misconfiguration | Security headers middleware (`nosniff`, frame denial, referrer policy, permissions policy, CSP, HSTS over TLS), per-environment CORS allowlist, no default credentials, non-root container, and `/metrics` plus `/debug/pprof` bound to a separate internal port. |
| API9 | Improper inventory management | The committed OpenAPI 3.1 document is the source of truth at `api/openapi.yaml`; `/docs` renders it and kin-openapi contract tests fail when a handler response drifts. |
| API10 | Unsafe consumption of third-party APIs | Elasticsearch, S3, SMTP and webhook calls have timeouts and bounded responses; Elasticsearch is derived data with a Postgres fallback; SMTP has a circuit breaker; webhook payloads are signed and delivery failures are recorded rather than retried forever. |

## Fixes made in this pass

- Added security headers, a CORS allowlist and a request body size limit.
- Raised the default bcrypt cost to 12 and made it configurable per environment.
- Added a per-account login backoff keyed by a SHA-256 hash of the email, on top
  of the existing per-IP login limiter.
- Audited SQL construction: all values are bound parameters; only static column
  fragments and placeholder numbers are interpolated.
- Moved metrics and pprof to a separate listener that is not published by
  compose or intended for a public reverse proxy.
- Added `govulncheck`, `gosec`, `gitleaks` and a Trivy image scan to CI, and
  enabled Dependabot for Go modules, GitHub Actions and Docker images.
