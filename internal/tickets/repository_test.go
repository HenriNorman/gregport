package tickets

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"
)

func TestCreateTicket_ConcurrentCreates_NoGapsNoDuplicates(t *testing.T) {
	pool := testPool(t)
	repo := NewRepository(pool)
	ctx := context.Background()

	// fresh org so the counter starts at 0
	var orgID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO organizations (name, slug) VALUES ('Concurrent Test', $1) RETURNING id`,
		"concurrent-"+uuid.NewString()[:8],
	).Scan(&orgID); err != nil {
		t.Fatalf("insert org: %v", err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM ticket_events WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(bg, `DELETE FROM comments WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(bg, `DELETE FROM tickets WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(bg, `DELETE FROM customers WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(bg, `DELETE FROM organizations WHERE id = $1`, orgID)
	})

	actor := Actor{Type: "system"}
	const N = 20
	var wg sync.WaitGroup
	errs := make(chan error, N)

	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, err := repo.CreateTicket(ctx, CreateInput{
				OrgID:          orgID,
				Actor:          actor,
				RequesterEmail: "req@test.local",
				RequesterName:  "Req",
				Subject:        "concurrent",
				Body:           "body",
				Priority:       "normal",
			})
			if err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("create failed: %v", err)
	}

	rows, err := pool.Query(ctx,
		`SELECT number FROM tickets WHERE org_id = $1 ORDER BY number`, orgID)
	if err != nil {
		t.Fatalf("query numbers: %v", err)
	}
	defer rows.Close()

	var numbers []int64
	for rows.Next() {
		var n int64
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		numbers = append(numbers, n)
	}
	if len(numbers) != N {
		t.Fatalf("expected %d tickets, got %d", N, len(numbers))
	}
	for i, n := range numbers {
		if n != int64(i+1) {
			t.Fatalf("expected number %d at position %d, got %d (numbers: %v)", i+1, i, n, numbers)
		}
	}
}

func TestCreateTicket_FailureLeavesNoGap(t *testing.T) {
	pool := testPool(t)
	repo := NewRepository(pool)
	ctx := context.Background()

	var orgID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO organizations (name, slug) VALUES ('Gap Test', $1) RETURNING id`,
		"gap-"+uuid.NewString()[:8],
	).Scan(&orgID); err != nil {
		t.Fatalf("insert org: %v", err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM ticket_events WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(bg, `DELETE FROM comments WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(bg, `DELETE FROM tickets WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(bg, `DELETE FROM customers WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(bg, `DELETE FROM organizations WHERE id = $1`, orgID)
	})

	actor := Actor{Type: "system"}

	// first: succeeds, number 1
	tk1, _, err := repo.CreateTicket(ctx, CreateInput{
		OrgID: orgID, Actor: actor,
		RequesterEmail: "a@test.local", RequesterName: "A",
		Subject: "one", Body: "body", Priority: "normal",
	})
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if tk1.Number != 1 {
		t.Fatalf("expected number 1, got %d", tk1.Number)
	}

	// second: force a failure by using an invalid priority (enum violation)
	_, _, err = repo.CreateTicket(ctx, CreateInput{
		OrgID: orgID, Actor: actor,
		RequesterEmail: "b@test.local", RequesterName: "B",
		Subject: "two", Body: "body", Priority: "not-a-priority",
	})
	if err == nil {
		t.Fatal("expected failure for invalid priority")
	}

	// third: succeeds, must be number 2 (no gap from the failed attempt)
	tk3, _, err := repo.CreateTicket(ctx, CreateInput{
		OrgID: orgID, Actor: actor,
		RequesterEmail: "c@test.local", RequesterName: "C",
		Subject: "three", Body: "body", Priority: "normal",
	})
	if err != nil {
		t.Fatalf("third: %v", err)
	}
	if tk3.Number != 2 {
		t.Fatalf("expected number 2 after failed attempt, got %d", tk3.Number)
	}
}
