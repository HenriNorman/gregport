# Backup and failure drills

Run on 25 November 2026 against the local compose stack: Postgres 16, Redis 7,
Elasticsearch 8.15.3, MinIO (object storage) and the API/worker binaries built
from this repository. Dataset: 1,000,049 tickets, 255 organisations and
2,496,500 comments.

## Backup restore

```bash
# Dump the live database in custom format.
docker exec gregport-db-1 pg_dump -U postgres -Fc gregport \
  > /tmp/opencode/gregport.dump

# Start a clean, empty Postgres instance on a different port.
docker run -d --name gregport-restore \
  -e POSTGRES_PASSWORD=restorepass \
  -e POSTGRES_DB=gregport_restore \
  -p 55432:5432 postgres:16-alpine
until PGPASSWORD=restorepass pg_isready -h localhost -p 55432 -U postgres; do sleep 1; done

# Restore without ownership or grants so the dump is portable.
PGPASSWORD=restorepass pg_restore -h localhost -p 55432 -U postgres \
  -d gregport_restore --no-owner --no-privileges /tmp/opencode/gregport.dump

# Verify the restored copy.
PGPASSWORD=restorepass psql -h localhost -p 55432 -U postgres -d gregport_restore \
  -c "select (select count(*) from tickets) as tickets,
             (select count(*) from organizations) as orgs,
             (select count(*) from comments) as comments;"
```

Result: 267 MB dump, restore completed in 1m30s. The restored database returned
1,000,049 tickets, 255 organisations and 2,496,500 comments, matching the source.
The application did not need to be stopped for the dump. Restoring into an
empty instance (rather than over the source) is the important part: it proves
the dump is self-contained and that schema, extensions and row data all survive.

## Dependency failures

Each dependency was stopped in turn, exercised through the public API, then
restarted. No API process was restarted unless noted.

### Redis

```bash
docker stop gregport-redis-1
```

| Probe | Result |
|:--|:--|
| `GET /healthz` | 200 |
| `GET /readyz` | 503 `{"status":"unready"}` |
| `GET /v1/tickets?limit=5` | 200, 1.76 s |
| `GET /v1/search?q=printer` | 200, 1.71 s |
| `GET /v1/dashboard/counts` | 200, 2.24 s |
| `POST /v1/auth/login` | 200, 5.23 s |

Redis is a readiness dependency, so the load balancer stops sending traffic
while it is down. Requests that do arrive still succeed: cache reads and writes
time out and fall through to Postgres, and the rate limiter logs
`rate limiter unavailable` and fails open. The cost is latency, not data loss.
Login is slowest because refresh-token and login-backoff writes also wait for
Redis.

Recovery after `docker start gregport-redis-1`: `/readyz` returned 200
immediately, list 1.8 ms, search 7 ms and login 60 ms. No API restart was
needed.

### Elasticsearch

```bash
docker stop gregport-elasticsearch-1
```

| Probe | Result |
|:--|:--|
| `GET /healthz` | 200 |
| `GET /readyz` | 200 |
| `GET /v1/search?q=printer` | 200, 4.26 s first call, then 1.28 s |

Elasticsearch is derived data, so it is not a readiness dependency. Search
falls back to the Postgres query when the Elasticsearch client fails. The first
call paid the client timeout; subsequent calls were faster but still an order
of magnitude slower than the indexed path. Results were complete and correctly
tenant-scoped.

Recovery after `docker start gregport-elasticsearch-1`: the next search returned
200 in 225 ms with no API restart. The search-index circuit used by the worker
stayed open for a short period afterwards, so queued `search:index_ticket` jobs
were skipped with `search index circuit open, skipping job`. Run
`go run ./cmd/reindex` after a longer Elasticsearch outage to rebuild derived
documents; the Postgres data was never at risk.

### MinIO

MinIO was already unavailable when the API started, which exercised the
startup path: the API logged `object storage unavailable at startup` and
continued serving.

| Probe | Result |
|:--|:--|
| `GET /healthz` | 200 |
| `GET /readyz` | 200 |
| `POST /v1/tickets/{number}/attachments/presign` | 201 (signing only, no storage call) |
| `POST /v1/attachments/{id}/confirm` | 500 after 3.36 s, `storage: head ... connection refused` |
| `GET /v1/attachments/{id}/download` | 409 `attachment_not_ready` |

Core ticketing, search and authentication were unaffected. Presigning succeeds
because it only signs a URL, but the upload to that URL fails while storage is
down, and confirmation cannot stat the object. The failed confirm left the
attachment in `pending`, so the client can retry the same confirm once storage
returns instead of re-uploading. The local environment had no MinIO image
available to pull, so recovery was not re-run here; the compose file defines
the service and the bucket initialiser for normal runs.

### Worker

```bash
# The API and database stay up; only the background consumer is stopped.
pkill -f gregport-worker
```

With the worker down, creating a ticket still returned 201 in 39 ms and wrote
one row to the transactional outbox. The outbox backlog was already 3,506
events before the test and grew to 3,507; no asynq task was pending because the
dispatcher runs in the worker.

Starting the worker drained the outbox from 3,507 to 0 in under 8 seconds
(`WORKER_OUTBOX_INTERVAL=1s`, batch 100). The worker's `/healthz` returned 200
and its metrics endpoint exposed `gregport_queue_depth` with zero pending and
active tasks afterwards. Ticket creation and reads never depend on the worker;
only notifications, webhooks, exports and index updates are delayed.

## What the drills show

- Postgres is the only hard dependency for reads and writes. Losing Redis
  removes readiness and adds seconds of latency; losing Elasticsearch falls
  back to Postgres; losing MinIO only breaks attachment confirmation; losing
  the worker delays asynchronous side effects.
- Every dependency failure produced an explicit log line and a bounded
  response, not a panic or a hung request.
- The outbox is the recovery mechanism for worker outages. Nothing is lost
  while the worker is down, and the backlog drains automatically on restart.
- A restore into a clean instance is a complete backup test. Counting rows in
  the restored database caught no drift from the source.
