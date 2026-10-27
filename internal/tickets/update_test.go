package tickets

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func makeTicket(t *testing.T, repo *Repository, orgID uuid.UUID) Ticket {
	t.Helper()
	tk, _, err := repo.CreateTicket(context.Background(), CreateInput{
		OrgID: orgID, Actor: Actor{Type: "system"},
		RequesterEmail: "req@test.local", RequesterName: "Req",
		Subject: "s", Body: "b", Priority: "normal",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return tk
}

func TestUpdateTicket_VersionMismatch(t *testing.T) {
	pool := testPool(t)
	repo := NewRepository(pool)
	orgID := freshOrg(t, pool)
	defer dropOrg(t, pool, orgID)

	tk := makeTicket(t, repo, orgID)
	status := "open"

	_, err := repo.UpdateTicket(context.Background(), UpdateInput{
		OrgID: orgID, Number: tk.Number, ExpectedVersion: tk.Version + 1,
		Actor: Actor{Type: "user"}, Status: &status,
	})
	if !errors.Is(err, ErrVersionMismatch) {
		t.Fatalf("expected ErrVersionMismatch, got %v", err)
	}
}

func TestUpdateTicket_InvalidTransition(t *testing.T) {
	pool := testPool(t)
	repo := NewRepository(pool)
	orgID := freshOrg(t, pool)
	defer dropOrg(t, pool, orgID)

	tk := makeTicket(t, repo, orgID) // status = new
	closed := "closed"

	_, err := repo.UpdateTicket(context.Background(), UpdateInput{
		OrgID: orgID, Number: tk.Number, ExpectedVersion: tk.Version,
		Actor: Actor{Type: "user"}, Status: &closed,
	})
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("expected ErrInvalidTransition, got %v", err)
	}
}

func TestUpdateTicket_ValidTransition(t *testing.T) {
	pool := testPool(t)
	repo := NewRepository(pool)
	orgID := freshOrg(t, pool)
	defer dropOrg(t, pool, orgID)

	tk := makeTicket(t, repo, orgID)
	open := "open"

	got, err := repo.UpdateTicket(context.Background(), UpdateInput{
		OrgID: orgID, Number: tk.Number, ExpectedVersion: tk.Version,
		Actor: Actor{Type: "user"}, Status: &open,
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if got.Status != "open" {
		t.Fatalf("expected open, got %s", got.Status)
	}
	if got.Version != tk.Version+1 {
		t.Fatalf("expected version %d, got %d", tk.Version+1, got.Version)
	}

	var count int
	_ = pool.QueryRow(context.Background(),
		`SELECT count(*) FROM ticket_events WHERE ticket_id = $1 AND event_type = 'ticket.status_changed'`,
		tk.ID).Scan(&count)
	if count != 1 {
		t.Fatalf("expected 1 status_changed event, got %d", count)
	}
}

func TestUpdateTicket_ConcurrentUpdates(t *testing.T) {
	pool := testPool(t)
	repo := NewRepository(pool)
	orgID := freshOrg(t, pool)
	defer dropOrg(t, pool, orgID)

	tk := makeTicket(t, repo, orgID)
	open := "open"

	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := repo.UpdateTicket(context.Background(), UpdateInput{
				OrgID: orgID, Number: tk.Number, ExpectedVersion: tk.Version,
				Actor: Actor{Type: "user"}, Status: &open,
			})
			results <- err
		}()
	}
	wg.Wait()
	close(results)

	var ok, mismatch int
	for err := range results {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ErrVersionMismatch):
			mismatch++
		default:
			t.Fatalf("unexpected: %v", err)
		}
	}
	if ok != 1 || mismatch != 1 {
		t.Fatalf("expected one success and one mismatch, got ok=%d mismatch=%d", ok, mismatch)
	}
}

func TestUpdateTicket_OtherOrg404(t *testing.T) {
	pool := testPool(t)
	repo := NewRepository(pool)
	orgA := freshOrg(t, pool)
	orgB := freshOrg(t, pool)
	defer dropOrg(t, pool, orgA)
	defer dropOrg(t, pool, orgB)

	tk := makeTicket(t, repo, orgA)
	open := "open"

	_, err := repo.UpdateTicket(context.Background(), UpdateInput{
		OrgID: orgB, Number: tk.Number, ExpectedVersion: tk.Version,
		Actor: Actor{Type: "user"}, Status: &open,
	})
	if !errors.Is(err, ErrTicketNotFound) {
		t.Fatalf("expected ErrTicketNotFound for other org, got %v", err)
	}
}

func freshOrg(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO organizations (name, slug) VALUES ('T', $1) RETURNING id`,
		"t-"+uuid.NewString()[:8],
	).Scan(&id); err != nil {
		t.Fatalf("org: %v", err)
	}
	return id
}

func dropOrg(t *testing.T, pool *pgxpool.Pool, id uuid.UUID) {
	t.Helper()
	bg := context.Background()
	_, _ = pool.Exec(bg, `DELETE FROM ticket_events WHERE org_id = $1`, id)
	_, _ = pool.Exec(bg, `DELETE FROM comments WHERE org_id = $1`, id)
	_, _ = pool.Exec(bg, `DELETE FROM tickets WHERE org_id = $1`, id)
	_, _ = pool.Exec(bg, `DELETE FROM customers WHERE org_id = $1`, id)
	_, _ = pool.Exec(bg, `DELETE FROM organizations WHERE id = $1`, id)
}
