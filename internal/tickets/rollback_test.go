package tickets

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func TestUpdateTicket_RollbackOnEventFailure(t *testing.T) {
	pool := testPool(t)
	repo := NewRepository(pool)
	ctx := context.Background()

	orgID := freshOrg(t, pool)
	defer dropOrg(t, pool, orgID)

	tk := makeTicket(t, repo, orgID) // status = new, version = 1

	_, err := pool.Exec(ctx,
		`CREATE OR REPLACE FUNCTION _test_fail_event() RETURNS trigger AS $$
		 BEGIN
		   IF NEW.org_id = '`+orgID.String()+`'::uuid THEN
		     RAISE EXCEPTION 'forced event failure';
		   END IF;
		   RETURN NEW;
		 END;
		 $$ LANGUAGE plpgsql;
		 CREATE TRIGGER _test_fail_event_trg
		   BEFORE INSERT ON ticket_events
		   FOR EACH ROW EXECUTE FUNCTION _test_fail_event();`)
	if err != nil {
		t.Fatalf("install trigger: %v", err)
	}
	defer func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DROP TRIGGER IF EXISTS _test_fail_event_trg ON ticket_events`)
		_, _ = pool.Exec(bg, `DROP FUNCTION IF EXISTS _test_fail_event()`)
	}()

	open := "open"
	_, err = repo.UpdateTicket(ctx, UpdateInput{
		OrgID:           orgID,
		Number:          tk.Number,
		ExpectedVersion: tk.Version,
		Actor:           Actor{Type: "user", ID: ptrUUID(uuid.New())},
		Status:          &open,
	})
	if err == nil {
		t.Fatal("expected update to fail when event insert fails")
	}

	// the ticket must still be 'new' at version 1 — the UPDATE rolled back
	after, err := repo.GetTicket(ctx, orgID, tk.Number)
	if err != nil {
		t.Fatalf("get after rollback: %v", err)
	}
	if after.Status != "new" {
		t.Fatalf("expected status 'new' after rollback, got %q", after.Status)
	}
	if after.Version != tk.Version {
		t.Fatalf("expected version %d after rollback, got %d", tk.Version, after.Version)
	}

	// no status_changed event should exist
	var count int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM ticket_events WHERE ticket_id = $1 AND event_type = 'ticket.status_changed'`,
		tk.ID,
	).Scan(&count); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 status_changed events after rollback, got %d", count)
	}
}

func ptrUUID(id uuid.UUID) *uuid.UUID { return &id }

var _ pgx.Tx
