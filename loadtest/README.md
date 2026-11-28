# Load tests

## What is measured

`baseline.js` runs four scenarios against the API:

| Scenario | Executor | Load |
|:--|:--|:--|
| Ticket list | ramping VUs | 0 → 10 → 50 → 0 over 2 minutes |
| Ticket detail | constant VUs | 20 VUs for 2 minutes |
| Ticket create | constant arrival rate | 5 requests/s for 2 minutes |
| Search | constant VUs | 10 VUs for 2 minutes |

The k6 process and the API share the same machine, so the absolute numbers are
conservative. The relative before/after numbers are the point.

## Setup

```bash
# 1. Skewed dataset: one 700k-ticket tenant and a long tail.
SEED_TICKETS=1000000 SEED_ORGS=12 SEED_CUSTOMERS=200 SEED_USERS=50 \
  SEED_SKEW=0.7 go run ./cmd/seed

# 2. Build the search index (search is served by Elasticsearch, with a
#    Postgres fallback when it is unavailable).
go run ./cmd/reindex

# 3. Run the API with the load-test rate limits raised so the limiter is not
#    the measured bottleneck. Production keeps the defaults.
APP_PORT=18080 METRICS_PORT=19091 \
  RATE_LIMIT_REQUESTS=1000000 PUBLIC_RATE_LIMIT_REQUESTS=1000000 \
  LOGIN_RATE_LIMIT_REQUESTS=10000 go run ./cmd/api

# 4. Run the same script before and after a change.
BASE_URL=http://127.0.0.1:18080 k6 run \
  --summary-trend-stats='avg,min,med,max,p(50),p(90),p(95),p(99)' \
  --summary-export=loadtest/results/baseline.json loadtest/k6/baseline.js
```

The committed runs used k6 v2.4.0, Go 1.27.4, Postgres 16-alpine, Redis 7 and
Elasticsearch 8.15.3 on an AMD Ryzen 5 7235HS (4 cores / 8 threads), 11 GiB RAM,
NVMe SSD. Dataset: 1,000,000 tickets, 12 organisations (largest 700,000),
2,496,499 comments. Each run lasted 2m10s.

## Before and after

The only schema change was `migrations/0012_ticket_list_index.up.sql`:

```sql
CREATE INDEX idx_tickets_org_created_active
    ON tickets (org_id, created_at DESC, id DESC)
    WHERE deleted_at IS NULL;
```

`EXPLAIN (ANALYZE, BUFFERS)` for the 25-row list of the 700k-ticket tenant:

| Plan | Execution time | Buffers |
|:--|--:|:--|
| Before: parallel seq scan + top-N sort | 208 ms | 594 hit, 30,816 read |
| After: index scan + memoized requester lookup | 0.52 ms | 104 hit, 3 read |

| Scenario | Metric | Before | After | Change | What changed |
|:--|:--|--:|--:|:--|:--|
| Ticket list | p50 | 4,899.53 ms | 16.68 ms | 294× faster | Partial composite index matches `org_id` + `created_at DESC, id DESC`; seq scan and sort removed |
| Ticket list | p95 | 8,530.99 ms | 40.19 ms | 212× faster | Same index |
| Ticket list | p99 | 9,763.88 ms | 58.87 ms | 166× faster | Same index |
| Ticket detail | p50 | 9.91 ms | 10.94 ms | ~same | No query change; the unique `(org_id, number)` index already served it, and less list contention offsets noise |
| Ticket detail | p95 | 37.87 ms | 30.23 ms | 20% faster | Lower database contention |
| Ticket detail | p99 | 352.65 ms | 44.88 ms | 7.9× faster | Lower database contention |
| Ticket create | p50 | 406.49 ms | 52.89 ms | 7.7× faster | Lower database contention during the transaction |
| Ticket create | p95 | 4,320 ms | 88.23 ms | 49× faster | Lower database contention |
| Ticket create | p99 | 4,740 ms | 116.69 ms | 40× faster | Lower database contention |
| Search | p50 | 61.50 ms | 47.74 ms | 22% faster | Lower database contention; Elasticsearch query unchanged |
| Search | p95 | 198.70 ms | 84.28 ms | 2.4× faster | Lower database contention |
| Search | p99 | 2,670 ms | 107.70 ms | 25× faster | Lower database contention |
| Whole run | Throughput | 696.5 req/s | 2,606.0 req/s | 3.7× higher | List requests stopped blocking for seconds |
| Whole run | Error rate | 0.05% | 0.00% | 51 → 0 failures | List requests no longer exceeded the 10s request timeout |

Raw k6 output is committed in `loadtest/results/`. k6 writes the access token
returned by `setup()` into the summary export; the committed copies have that
field redacted, and `.gitleaks.toml` excludes these generated artifacts from
secret scanning.

## Notes

- The first attempt without raising the rate limits was invalid: 6,000 req/s
  tripped the 100 requests/minute user limiter and 89% of responses were 429.
  Those results are not committed. This is the expected behaviour of the
  production limiter, not an API failure.
- No N+1 was found in the list path. The requester is joined in the same query;
  the plan above shows one index scan and one memoized lookup for 25 rows.
- The existing read-through cache covers organisation settings, SLA policies and
  dashboard counts. Cache hits and misses are exported as Prometheus counters so
  the Grafana dashboard can show the hit ratio.
