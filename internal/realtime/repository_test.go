package realtime

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func replayTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), "postgres://postgres:Henri123@localhost:5432/gregport?sslmode=disable")
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestRepositoryReplayIsOrgScopedAndFiltered(t *testing.T) {
	pool := replayTestPool(t)
	repo := NewRepository(pool)
	ctx := context.Background()

	orgA := seedReplayOrg(t, pool)
	orgB := seedReplayOrg(t, pool)
	ticketA := seedReplayTicket(t, pool, orgA, 42)
	ticketB := seedReplayTicket(t, pool, orgB, 42)

	first := seedReplayEvent(t, pool, orgA, ticketA, "ticket.created", `{"number":42,"internal":"hidden"}`)
	seedReplayEvent(t, pool, orgA, ticketA, "ticket.status_changed", `{"old":"open","new":"pending","secret":"x"}`)
	seedReplayEvent(t, pool, orgA, ticketA, "unknown.event", `{"anything":true}`)
	seedReplayEvent(t, pool, orgB, ticketB, "ticket.created", `{"number":42}`)

	// Replay after the first event: two events for A, none for B.
	events, err := repo.Replay(ctx, orgA, first, 10)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events: got %d, want 2", len(events))
	}

	status := events[0]
	if status.Type != "ticket.status_changed" {
		t.Fatalf("first type: %q", status.Type)
	}
	if status.TicketNumber != 42 {
		t.Fatalf("ticket number: %d", status.TicketNumber)
	}
	if string(status.Data) != `{"new":"pending","old":"open"}` {
		t.Fatalf("filtered data: %s", status.Data)
	}

	unknown := events[1]
	if string(unknown.Data) != `{}` {
		t.Fatalf("unknown event data: %s", unknown.Data)
	}

	// A cursor at the newest event yields nothing.
	afterAll, err := repo.Replay(ctx, orgA, unknown.Seq, 10)
	if err != nil {
		t.Fatalf("replay empty: %v", err)
	}
	if len(afterAll) != 0 {
		t.Fatalf("events after newest: %d", len(afterAll))
	}
}

func TestRepositoryTicketExistsIsOrgScoped(t *testing.T) {
	pool := replayTestPool(t)
	repo := NewRepository(pool)
	ctx := context.Background()

	orgA := seedReplayOrg(t, pool)
	orgB := seedReplayOrg(t, pool)
	seedReplayTicket(t, pool, orgA, 7)

	ok, err := repo.TicketExists(ctx, orgA, 7)
	if err != nil || !ok {
		t.Fatalf("own ticket: ok=%v err=%v", ok, err)
	}
	ok, err = repo.TicketExists(ctx, orgB, 7)
	if err != nil || ok {
		t.Fatalf("other org ticket: ok=%v err=%v", ok, err)
	}
}

func seedReplayOrg(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO organizations (name, slug) VALUES ('Realtime Test', $1) RETURNING id`,
		"realtime-"+uuid.NewString()[:8],
	).Scan(&id); err != nil {
		t.Fatalf("seed org: %v", err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM ticket_events WHERE org_id = $1`, id)
		_, _ = pool.Exec(bg, `DELETE FROM comments WHERE org_id = $1`, id)
		_, _ = pool.Exec(bg, `DELETE FROM tickets WHERE org_id = $1`, id)
		_, _ = pool.Exec(bg, `DELETE FROM customers WHERE org_id = $1`, id)
		_, _ = pool.Exec(bg, `DELETE FROM organizations WHERE id = $1`, id)
	})
	return id
}

func seedReplayTicket(t *testing.T, pool *pgxpool.Pool, orgID uuid.UUID, number int64) uuid.UUID {
	t.Helper()
	var customerID uuid.UUID
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO customers (org_id, email) VALUES ($1, $2) RETURNING id`,
		orgID, uuid.NewString()+"@test.local",
	).Scan(&customerID); err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	var ticketID uuid.UUID
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO tickets (org_id, number, requester_id, subject)
		 VALUES ($1, $2, $3, 'replay') RETURNING id`,
		orgID, number, customerID,
	).Scan(&ticketID); err != nil {
		t.Fatalf("seed ticket: %v", err)
	}
	return ticketID
}

func seedReplayEvent(t *testing.T, pool *pgxpool.Pool, orgID, ticketID uuid.UUID, eventType, data string) int64 {
	t.Helper()
	var seq int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO ticket_events (org_id, ticket_id, actor_type, event_type, data)
		 VALUES ($1, $2, 'system', $3, $4) RETURNING seq`,
		orgID, ticketID, eventType, data,
	).Scan(&seq); err != nil {
		t.Fatalf("seed event: %v", err)
	}
	return seq
}
