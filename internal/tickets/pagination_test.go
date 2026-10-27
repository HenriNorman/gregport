package tickets

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func seedTickets(t *testing.T, repo *Repository, orgID uuid.UUID, n int) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < n; i++ {
		if _, _, err := repo.CreateTicket(ctx, CreateInput{
			OrgID:          orgID,
			Actor:          Actor{Type: "system"},
			RequesterEmail: "req@test.local",
			RequesterName:  "Req",
			Subject:        "seed",
			Body:           "body",
			Priority:       "normal",
		}); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
}

func TestListTickets_PaginationReturnsEveryRowOnce(t *testing.T) {
	pool := testPool(t)
	repo := NewRepository(pool)
	ctx := context.Background()

	var orgID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO organizations (name, slug) VALUES ('Pag Test', $1) RETURNING id`,
		"pag-"+uuid.NewString()[:8],
	).Scan(&orgID); err != nil {
		t.Fatalf("org: %v", err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM ticket_events WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(bg, `DELETE FROM comments WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(bg, `DELETE FROM tickets WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(bg, `DELETE FROM customers WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(bg, `DELETE FROM organizations WHERE id = $1`, orgID)
	})

	seedTickets(t, repo, orgID, 25)

	seen := map[uuid.UUID]bool{}
	cursor := ""
	pages := 0
	for {
		res, err := repo.ListTickets(ctx, orgID, ListFilter{Limit: 10, Cursor: cursor})
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		pages++
		for _, tk := range res.Tickets {
			if seen[tk.ID] {
				t.Fatalf("duplicate ticket %v", tk.ID)
			}
			seen[tk.ID] = true
		}
		if res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
		if pages > 10 {
			t.Fatal("too many pages — cursor loop")
		}
	}

	if len(seen) != 25 {
		t.Fatalf("expected 25 unique tickets, got %d", len(seen))
	}
	if pages != 3 {
		t.Fatalf("expected 3 pages (10+10+5), got %d", pages)
	}
}

func TestListTickets_StableWhileInserting(t *testing.T) {
	pool := testPool(t)
	repo := NewRepository(pool)
	ctx := context.Background()

	var orgID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO organizations (name, slug) VALUES ('Stable Test', $1) RETURNING id`,
		"stable-"+uuid.NewString()[:8],
	).Scan(&orgID); err != nil {
		t.Fatalf("org: %v", err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM ticket_events WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(bg, `DELETE FROM comments WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(bg, `DELETE FROM tickets WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(bg, `DELETE FROM customers WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(bg, `DELETE FROM organizations WHERE id = $1`, orgID)
	})

	seedTickets(t, repo, orgID, 15)

	// first page
	first, err := repo.ListTickets(ctx, orgID, ListFilter{Limit: 10})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if len(first.Tickets) != 10 {
		t.Fatalf("expected 10, got %d", len(first.Tickets))
	}

	// insert 5 new tickets between pages
	seedTickets(t, repo, orgID, 5)

	// second page using the first page's cursor
	second, err := repo.ListTickets(ctx, orgID, ListFilter{Limit: 10, Cursor: first.NextCursor})
	if err != nil {
		t.Fatalf("second page: %v", err)
	}

	// all 10 from page 2 must be different from page 1's 10
	firstIDs := map[uuid.UUID]bool{}
	for _, tk := range first.Tickets {
		firstIDs[tk.ID] = true
	}
	for _, tk := range second.Tickets {
		if firstIDs[tk.ID] {
			t.Fatalf("page 2 contains page 1 ticket %v — keyset is not stable", tk.ID)
		}
	}
	// page 2 must have 5 (the original remaining) — the 5 new ones are *newer*,
	// so they sort before page 1's cursor and are not included
	if len(second.Tickets) != 5 {
		t.Fatalf("expected 5 on page 2, got %d", len(second.Tickets))
	}
}
