// Command benchsearch measures search latency for the Postgres full-text
// baseline and the Elasticsearch backend on the same seeded corpus.
//
// Both backends implement search.Backend, so the benchmark exercises exactly
// the same entry point the API uses: identical queries, filters, and result
// limit, including the Postgres hydration step for Elasticsearch hits. That
// makes the comparison a like-for-like measure of the full search path rather
// than just the query engine.
//
// Usage:
//
//	go run ./cmd/benchsearch
//
// Environment:
//
//	BENCH_ORG          organisation slug (default: the seeded org with most tickets)
//	BENCH_ITERATIONS   measured iterations per query per backend (default 50)
//	BENCH_WARMUP       warmup iterations per query per backend (default 5)
//	BENCH_LIMIT        result limit (default 25)
//	BENCH_QUERIES      comma-separated queries (default realistic set below)
//
// It prints per-query and overall p50/p95 latency in milliseconds.
package main

import (
	"context"
	"fmt"
	"log"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"

	"github.com/HenriNorman/gregport/internal/platform/config"
	"github.com/HenriNorman/gregport/internal/platform/database"
	"github.com/HenriNorman/gregport/internal/search"
)

var defaultQueries = []string{
	"printer",
	"cannot log in",
	"invoice",
	"api error",
	"crash on startup",
	"password reset",
	"refund",
	"attachment",
	"export records",
	"webhook",
}

func main() {
	if err := godotenv.Load(); err != nil {
		log.Println("no .env file, relying on real env vars")
	}
	cfg, err := config.LoadWorker()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	pool, err := database.NewPool(ctx, cfg.DSN())
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer pool.Close()

	esClient, err := search.NewClient(search.ClientConfig{
		URL:      cfg.ElasticsearchURL,
		Index:    cfg.ElasticsearchIndex,
		Username: cfg.ElasticsearchUsername,
		Password: cfg.ElasticsearchPassword,
		APIKey:   cfg.ElasticsearchAPIKey,
		Timeout:  cfg.ElasticsearchTimeout,
	})
	if err != nil {
		log.Fatalf("search client: %v", err)
	}
	if err := esClient.Ping(ctx); err != nil {
		log.Fatalf("elasticsearch unavailable: %v", err)
	}

	orgID, orgSlug, err := resolveOrg(ctx, pool, os.Getenv("BENCH_ORG"))
	if err != nil {
		log.Fatalf("resolve org: %v", err)
	}

	iterations := envInt("BENCH_ITERATIONS", 50)
	warmup := envInt("BENCH_WARMUP", 5)
	limit := envInt("BENCH_LIMIT", 25)
	queries := defaultQueries
	if raw := os.Getenv("BENCH_QUERIES"); raw != "" {
		queries = strings.Split(raw, ",")
	}

	pg := search.NewRepository(pool)
	es := search.NewElasticBackend(esClient, pg)

	backends := []struct {
		name    string
		backend search.Backend
	}{
		{"postgres", pg},
		{"elastic", es},
	}

	log.Printf("org=%s id=%s iterations=%d warmup=%d limit=%d queries=%d",
		orgSlug, orgID, iterations, warmup, limit, len(queries))

	fmt.Printf("\n%-22s %-9s %6s %10s %10s %10s %6s\n",
		"query", "backend", "n", "p50_ms", "p95_ms", "mean_ms", "hits")

	overall := make(map[string][]time.Duration)

	for _, q := range queries {
		filter := search.Filter{Query: strings.TrimSpace(q), Limit: limit}

		for _, b := range backends {
			// Warm the path so cold connections and caches do not dominate the
			// first measured sample.
			for i := 0; i < warmup; i++ {
				if _, err := b.backend.Search(ctx, orgID, filter); err != nil {
					log.Fatalf("%s warmup %q: %v", b.name, q, err)
				}
			}

			var latencies []time.Duration
			hits := 0
			for i := 0; i < iterations; i++ {
				started := time.Now()
				res, err := b.backend.Search(ctx, orgID, filter)
				elapsed := time.Since(started)
				if err != nil {
					log.Fatalf("%s search %q: %v", b.name, q, err)
				}
				hits = len(res.Hits)
				latencies = append(latencies, elapsed)
			}
			overall[b.name] = append(overall[b.name], latencies...)

			fmt.Printf("%-22s %-9s %6d %10.2f %10.2f %10.2f %6d\n",
				q, b.name, len(latencies),
				ms(percentile(latencies, 0.50)),
				ms(percentile(latencies, 0.95)),
				ms(mean(latencies)),
				hits,
			)
		}
	}

	fmt.Printf("\n%-22s %-9s %6s %10s %10s %10s\n",
		"overall", "backend", "n", "p50_ms", "p95_ms", "mean_ms")
	for _, b := range backends {
		latencies := overall[b.name]
		fmt.Printf("%-22s %-9s %6d %10.2f %10.2f %10.2f\n",
			"all queries", b.name, len(latencies),
			ms(percentile(latencies, 0.50)),
			ms(percentile(latencies, 0.95)),
			ms(mean(latencies)),
		)
	}
}

// resolveOrg returns the requested org, or the org with the most non-deleted
// tickets when no slug is given. The benchmark then exercises a realistic,
// well-populated tenant instead of an empty one.
func resolveOrg(ctx context.Context, pool *pgxpool.Pool, slug string) (uuid.UUID, string, error) {
	var (
		id      uuid.UUID
		gotSlug string
	)
	if slug != "" {
		err := pool.QueryRow(ctx,
			`SELECT id, slug FROM organizations WHERE slug = $1 AND deleted_at IS NULL`,
			slug,
		).Scan(&id, &gotSlug)
		if err == pgx.ErrNoRows {
			return uuid.Nil, "", fmt.Errorf("organisation %q not found", slug)
		}
		if err != nil {
			return uuid.Nil, "", err
		}
		return id, gotSlug, nil
	}

	err := pool.QueryRow(ctx, `
		SELECT o.id, o.slug
		FROM organizations o
		JOIN tickets t ON t.org_id = o.id AND t.deleted_at IS NULL
		WHERE o.deleted_at IS NULL
		GROUP BY o.id, o.slug
		ORDER BY count(*) DESC
		LIMIT 1`,
	).Scan(&id, &gotSlug)
	if err != nil {
		return uuid.Nil, "", fmt.Errorf("find populated organisation: %w", err)
	}
	return id, gotSlug, nil
}

func envInt(name string, fallback int) int {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		log.Fatalf("%s: %v", name, err)
	}
	return v
}

func percentile(latencies []time.Duration, p float64) time.Duration {
	if len(latencies) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), latencies...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	rank := int(math.Ceil(p*float64(len(sorted)))) - 1
	if rank < 0 {
		rank = 0
	}
	if rank >= len(sorted) {
		rank = len(sorted) - 1
	}
	return sorted[rank]
}

func mean(latencies []time.Duration) time.Duration {
	if len(latencies) == 0 {
		return 0
	}
	var total time.Duration
	for _, l := range latencies {
		total += l
	}
	return total / time.Duration(len(latencies))
}

func ms(d time.Duration) float64 {
	return float64(d.Microseconds()) / 1000
}
