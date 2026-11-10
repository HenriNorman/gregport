package search

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/HenriNorman/gregport/internal/platform/requestctx"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, "postgres://postgres:Henri123@localhost:5432/gregport?sslmode=disable")
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func seedOrg(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO organizations (name, slug) VALUES ('Search Test', $1) RETURNING id`,
		"search-"+uuid.NewString()[:8],
	).Scan(&id); err != nil {
		t.Fatalf("seed org: %v", err)
	}
	return id
}

func seedUser(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO users (email, password_hash, name) VALUES ($1, 'x', 'Search User') RETURNING id`,
		"search-"+uuid.NewString()[:8]+"@test.local",
	).Scan(&id); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return id
}

func seedCustomer(t *testing.T, pool *pgxpool.Pool, orgID uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO customers (org_id, email, name) VALUES ($1, $2, 'Search Customer') RETURNING id`,
		orgID, "customer-"+uuid.NewString()[:8]+"@test.local",
	).Scan(&id); err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	return id
}

func seedTicket(t *testing.T, pool *pgxpool.Pool, orgID uuid.UUID, number int64,
	requesterID uuid.UUID, assigneeID *uuid.UUID, subject, status string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO tickets (org_id, number, requester_id, assignee_id, subject, status, priority, channel)
		 VALUES ($1, $2, $3, $4, $5, $6, 'normal', 'api') RETURNING id`,
		orgID, number, requesterID, assigneeID, subject, status,
	).Scan(&id); err != nil {
		t.Fatalf("seed ticket: %v", err)
	}
	return id
}

func seedComment(t *testing.T, pool *pgxpool.Pool, orgID, ticketID uuid.UUID, body string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO comments (org_id, ticket_id, author_type, body)
		 VALUES ($1, $2, 'customer', $3) RETURNING id`,
		orgID, ticketID, body,
	).Scan(&id); err != nil {
		t.Fatalf("seed comment: %v", err)
	}
	return id
}

func seedTag(t *testing.T, pool *pgxpool.Pool, orgID, ticketID uuid.UUID, name string) {
	t.Helper()
	var tagID uuid.UUID
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO tags (org_id, name) VALUES ($1, $2) RETURNING id`,
		orgID, name,
	).Scan(&tagID); err != nil {
		t.Fatalf("seed tag: %v", err)
	}
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO ticket_tags (ticket_id, tag_id) VALUES ($1, $2)`,
		ticketID, tagID,
	); err != nil {
		t.Fatalf("attach tag: %v", err)
	}
}

func cleanupOrg(t *testing.T, pool *pgxpool.Pool, orgID, userID uuid.UUID) {
	t.Helper()
	bg := context.Background()
	_, _ = pool.Exec(bg, `DELETE FROM ticket_tags WHERE ticket_id IN (SELECT id FROM tickets WHERE org_id = $1)`, orgID)
	_, _ = pool.Exec(bg, `DELETE FROM comments WHERE org_id = $1`, orgID)
	_, _ = pool.Exec(bg, `DELETE FROM tickets WHERE org_id = $1`, orgID)
	_, _ = pool.Exec(bg, `DELETE FROM tags WHERE org_id = $1`, orgID)
	_, _ = pool.Exec(bg, `DELETE FROM customers WHERE org_id = $1`, orgID)
	_, _ = pool.Exec(bg, `DELETE FROM organizations WHERE id = $1`, orgID)
	_, _ = pool.Exec(bg, `DELETE FROM memberships WHERE user_id = $1`, userID)
	_, _ = pool.Exec(bg, `DELETE FROM users WHERE id = $1`, userID)
}

func TestSearchRequiresOrg(t *testing.T) {
	pool := testPool(t)
	repo := NewRepository(pool)
	if _, err := repo.Search(context.Background(), uuid.Nil, Filter{Query: "anything"}); !errors.Is(err, ErrMissingOrg) {
		t.Fatalf("Search without org = %v, want ErrMissingOrg", err)
	}
}

func TestPostgresSearchScopesAndHighlights(t *testing.T) {
	if testing.Short() {
		t.Skip("postgres integration test")
	}
	pool := testPool(t)
	ctx := context.Background()
	svc := NewService(NewRepository(pool))

	orgA := seedOrg(t, pool)
	orgB := seedOrg(t, pool)
	userA := seedUser(t, pool)
	userB := seedUser(t, pool)
	custA := seedCustomer(t, pool, orgA)
	custB := seedCustomer(t, pool, orgB)
	t.Cleanup(func() {
		cleanupOrg(t, pool, orgA, userA)
		cleanupOrg(t, pool, orgB, userB)
	})

	ticketA := seedTicket(t, pool, orgA, 1, custA, &userA, "Printer cannot print", "open")
	seedTicket(t, pool, orgA, 2, custA, nil, "Network outage in office", "pending")
	ticketB := seedTicket(t, pool, orgB, 1, custB, &userB, "Printer cannot print", "open")
	seedComment(t, pool, orgA, ticketA, "The paper tray is jammed and the roller squeaks")
	seedTag(t, pool, orgA, ticketA, "hardware")

	pA := requestctx.Principal{Kind: requestctx.KindUser, ActorID: userA, OrgID: orgA, Role: "agent"}
	pB := requestctx.Principal{Kind: requestctx.KindUser, ActorID: userB, OrgID: orgB, Role: "agent"}

	t.Run("subject match is org scoped and highlighted", func(t *testing.T) {
		res, err := svc.Search(ctx, pA, Filter{Query: "printer", Limit: 25})
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if len(res.Hits) != 1 {
			t.Fatalf("hits = %d, want 1", len(res.Hits))
		}
		if res.Hits[0].ID != ticketA {
			t.Fatalf("hit id = %s, want %s", res.Hits[0].ID, ticketA)
		}
		if !strings.Contains(res.Hits[0].SubjectHighlight, "<mark>") {
			t.Fatalf("subject highlight = %q, want a mark", res.Hits[0].SubjectHighlight)
		}
	})

	t.Run("org B only sees its own hit", func(t *testing.T) {
		res, err := svc.Search(ctx, pB, Filter{Query: "printer", Limit: 25})
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if len(res.Hits) != 1 || res.Hits[0].ID != ticketB {
			t.Fatalf("org B hits = %+v, want only %s", res.Hits, ticketB)
		}
	})

	t.Run("comment match carries a comment highlight", func(t *testing.T) {
		res, err := svc.Search(ctx, pA, Filter{Query: "paper tray", Limit: 25})
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if len(res.Hits) != 1 || res.Hits[0].ID != ticketA {
			t.Fatalf("comment hits = %+v, want %s", res.Hits, ticketA)
		}
		if !strings.Contains(res.Hits[0].CommentHighlight, "<mark>") {
			t.Fatalf("comment highlight = %q, want a mark", res.Hits[0].CommentHighlight)
		}
	})

	t.Run("facets filter status tag and assignee", func(t *testing.T) {
		status := "pending"
		res, err := svc.Search(ctx, pA, Filter{Status: &status, Limit: 25})
		if err != nil {
			t.Fatalf("status search: %v", err)
		}
		if len(res.Hits) != 1 || res.Hits[0].Status != "pending" {
			t.Fatalf("status hits = %+v", res.Hits)
		}

		tag := "hardware"
		res, err = svc.Search(ctx, pA, Filter{Query: "printer", Tag: &tag, Limit: 25})
		if err != nil {
			t.Fatalf("tag search: %v", err)
		}
		if len(res.Hits) != 1 || res.Hits[0].ID != ticketA {
			t.Fatalf("tag hits = %+v, want %s", res.Hits, ticketA)
		}

		res, err = svc.Search(ctx, pA, Filter{Query: "printer", AssigneeID: &userA, Limit: 25})
		if err != nil {
			t.Fatalf("assignee search: %v", err)
		}
		if len(res.Hits) != 1 || res.Hits[0].ID != ticketA {
			t.Fatalf("assignee hits = %+v, want %s", res.Hits, ticketA)
		}
	})

	t.Run("soft deleted tickets disappear", func(t *testing.T) {
		if _, err := pool.Exec(ctx, `UPDATE tickets SET deleted_at = now() WHERE id = $1`, ticketA); err != nil {
			t.Fatalf("soft delete: %v", err)
		}
		res, err := svc.Search(ctx, pA, Filter{Query: "printer", Limit: 25})
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if len(res.Hits) != 0 {
			t.Fatalf("hits after delete = %+v, want none", res.Hits)
		}
		res, err = svc.Search(ctx, pA, Filter{Limit: 25})
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(res.Hits) != 1 || res.Hits[0].Number != 2 {
			t.Fatalf("listing after delete = %+v, want only ticket 2", res.Hits)
		}
	})
}

// BenchmarkPostgresSearch measures the baseline against a seeded dataset.
// It is skipped unless SEARCH_BENCH_ORG points at an org with realistic data;
// the seed tool in cmd/seed creates one. Step 10 records p50/p95 from this
// harness and compares it with Elasticsearch.
func BenchmarkPostgresSearch(b *testing.B) {
	orgRaw := os.Getenv("SEARCH_BENCH_ORG")
	if orgRaw == "" {
		b.Skip("set SEARCH_BENCH_ORG to a seeded org id")
	}
	orgID, err := uuid.Parse(orgRaw)
	if err != nil {
		b.Fatalf("SEARCH_BENCH_ORG: %v", err)
	}
	query := os.Getenv("SEARCH_BENCH_QUERY")
	if query == "" {
		query = "printer"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, "postgres://postgres:Henri123@localhost:5432/gregport?sslmode=disable")
	if err != nil {
		b.Fatalf("pool: %v", err)
	}
	defer pool.Close()

	svc := NewService(NewRepository(pool))
	p := requestctx.Principal{Kind: requestctx.KindUser, OrgID: orgID, Role: "agent"}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := svc.Search(ctx, p, Filter{Query: query, Limit: 25}); err != nil {
			b.Fatalf("search: %v", err)
		}
	}
}

func TestTicketsByIDsScopesAndSkipsDeleted(t *testing.T) {
	if testing.Short() {
		t.Skip("postgres integration test")
	}
	pool := testPool(t)
	ctx := context.Background()
	repo := NewRepository(pool)

	orgA := seedOrg(t, pool)
	orgB := seedOrg(t, pool)
	userA := seedUser(t, pool)
	userB := seedUser(t, pool)
	custA := seedCustomer(t, pool, orgA)
	custB := seedCustomer(t, pool, orgB)
	t.Cleanup(func() {
		cleanupOrg(t, pool, orgA, userA)
		cleanupOrg(t, pool, orgB, userB)
	})

	ticketA := seedTicket(t, pool, orgA, 1, custA, &userA, "Visible ticket", "open")
	deletedA := seedTicket(t, pool, orgA, 2, custA, nil, "Deleted ticket", "open")
	ticketB := seedTicket(t, pool, orgB, 1, custB, nil, "Foreign ticket", "open")
	if _, err := pool.Exec(ctx, `UPDATE tickets SET deleted_at = now() WHERE id = $1`, deletedA); err != nil {
		t.Fatalf("soft delete: %v", err)
	}

	hits, err := repo.TicketsByIDs(ctx, orgA, []uuid.UUID{ticketA, deletedA, ticketB})
	if err != nil {
		t.Fatalf("TicketsByIDs: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("hits = %+v, want only the visible org A ticket", hits)
	}
	got, ok := hits[ticketA]
	if !ok {
		t.Fatalf("missing visible ticket %s", ticketA)
	}
	if got.Number != 1 || got.Subject != "Visible ticket" || got.OrgID != orgA {
		t.Fatalf("hydrated hit = %+v", got)
	}
}

func TestTicketsByIDsRequiresOrg(t *testing.T) {
	pool := testPool(t)
	repo := NewRepository(pool)
	if _, err := repo.TicketsByIDs(context.Background(), uuid.Nil, []uuid.UUID{uuid.New()}); !errors.Is(err, ErrMissingOrg) {
		t.Fatalf("TicketsByIDs without org = %v, want ErrMissingOrg", err)
	}
}

func TestPostgresSearchPagination(t *testing.T) {
	if testing.Short() {
		t.Skip("postgres integration test")
	}
	pool := testPool(t)
	ctx := context.Background()
	svc := NewService(NewRepository(pool))

	orgID := seedOrg(t, pool)
	userID := seedUser(t, pool)
	custID := seedCustomer(t, pool, orgID)
	t.Cleanup(func() { cleanupOrg(t, pool, orgID, userID) })

	want := map[uuid.UUID]bool{}
	for i := 1; i <= 3; i++ {
		id := seedTicket(t, pool, orgID, int64(i), custID, nil, "widget failure number", "open")
		want[id] = true
	}

	p := requestctx.Principal{Kind: requestctx.KindUser, ActorID: userID, OrgID: orgID, Role: "agent"}
	seen := map[uuid.UUID]bool{}
	cursor := ""
	for page := 0; page < 10; page++ {
		res, err := svc.Search(ctx, p, Filter{Query: "widget", Cursor: cursor, Limit: 1})
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		for _, h := range res.Hits {
			if seen[h.ID] {
				t.Fatalf("duplicate hit %s on page %d", h.ID, page)
			}
			seen[h.ID] = true
		}
		if res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}

	if len(seen) != len(want) {
		t.Fatalf("pagination saw %d hits, want %d", len(seen), len(want))
	}
	for id := range want {
		if !seen[id] {
			t.Fatalf("pagination missed %s", id)
		}
	}
}
