package tickets

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

type recordingDeleter struct {
	keys []string
	err  error
}

func (d *recordingDeleter) Delete(_ context.Context, key string) error {
	if d.err != nil {
		return d.err
	}
	d.keys = append(d.keys, key)
	return nil
}

func TestPurgeDeletedTicketsHardDeletesAndRemovesObjects(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	repo := NewRepository(pool)

	orgID := uuid.New()
	customerID := uuid.New()
	userID := uuid.New()
	oldTicket := uuid.New()
	recentTicket := uuid.New()
	commentID := uuid.New()

	if _, err := pool.Exec(ctx,
		`INSERT INTO organizations (id, name, slug) VALUES ($1, 'Retention Test', $2)`,
		orgID, "retention-"+orgID.String()[:8],
	); err != nil {
		t.Fatalf("seed org: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO users (id, email, password_hash, name) VALUES ($1, $2, 'x', 'Retention User')`,
		userID, "retention-"+userID.String()[:8]+"@test.local",
	); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO memberships (org_id, user_id, role) VALUES ($1, $2, 'admin')`,
		orgID, userID,
	); err != nil {
		t.Fatalf("seed membership: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO customers (id, org_id, email, name) VALUES ($1, $2, $3, 'Retention Customer')`,
		customerID, orgID, "retention-customer-"+customerID.String()[:8]+"@test.local",
	); err != nil {
		t.Fatalf("seed customer: %v", err)
	}

	if _, err := pool.Exec(ctx,
		`INSERT INTO tickets (id, org_id, number, requester_id, subject, status, priority, channel, deleted_at)
		 VALUES ($1, $2, 1, $3, 'Old ticket', 'open', 'normal', 'api', now() - interval '31 days'),
		        ($4, $2, 2, $3, 'Recent ticket', 'open', 'normal', 'api', now() - interval '1 day')`,
		oldTicket, orgID, customerID, recentTicket,
	); err != nil {
		t.Fatalf("seed tickets: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO comments (id, org_id, ticket_id, author_type, body)
		 VALUES ($1, $2, $3, 'customer', 'hello')`,
		commentID, orgID, oldTicket,
	); err != nil {
		t.Fatalf("seed comment: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO ticket_events (org_id, ticket_id, actor_type, event_type)
		 VALUES ($1, $2, 'system', 'ticket.created')`,
		orgID, oldTicket,
	); err != nil {
		t.Fatalf("seed event: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO attachments (org_id, ticket_id, comment_id, storage_key, filename, mime_type, size_bytes, upload_status, thumbnail_key)
		 VALUES ($1, $2, $3, $4, 'a.pdf', 'application/pdf', 10, 'uploaded', $5)`,
		orgID, oldTicket, commentID, "org/"+orgID.String()+"/tickets/"+oldTicket.String()+"/original",
		"org/"+orgID.String()+"/tickets/"+oldTicket.String()+"/thumbnails/thumb.jpg",
	); err != nil {
		t.Fatalf("seed attachment: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO notifications (org_id, user_id, type, ticket_id, comment_id)
		 VALUES ($1, $2, 'new_comment', $3, $4)`,
		orgID, userID, oldTicket, commentID,
	); err != nil {
		t.Fatalf("seed notification: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO ticket_ratings (org_id, ticket_id, customer_id, score) VALUES ($1, $2, $3, 5)`,
		orgID, oldTicket, customerID,
	); err != nil {
		t.Fatalf("seed rating: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO satisfaction_ratings (org_id, ticket_id, customer_id, score) VALUES ($1, $2, $3, 4)`,
		orgID, oldTicket, customerID,
	); err != nil {
		t.Fatalf("seed legacy rating: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO inbound_emails (org_id, message_id, from_email, recipient, subject, body, raw, ticket_id, comment_id)
		 VALUES ($1, $2, 'c@test.local', 'support@test.local', 'Re: old', 'body', 'raw', $3, $4)`,
		orgID, "retention-"+uuid.NewString(), oldTicket, commentID,
	); err != nil {
		t.Fatalf("seed inbound: %v", err)
	}

	t.Cleanup(func() {
		bg := context.Background()
		for _, q := range []string{
			`DELETE FROM inbound_emails WHERE org_id = $1`,
			`DELETE FROM notifications WHERE org_id = $1`,
			`DELETE FROM attachments WHERE org_id = $1`,
			`DELETE FROM satisfaction_ratings WHERE org_id = $1`,
			`DELETE FROM ticket_ratings WHERE org_id = $1`,
			`DELETE FROM ticket_events WHERE org_id = $1`,
			`DELETE FROM comments WHERE org_id = $1`,
			`DELETE FROM tickets WHERE org_id = $1`,
			`DELETE FROM customers WHERE org_id = $1`,
			`DELETE FROM memberships WHERE org_id = $1`,
		} {
			_, _ = pool.Exec(bg, q, orgID)
		}
		_, _ = pool.Exec(bg, `DELETE FROM users WHERE id = $1`, userID)
		_, _ = pool.Exec(bg, `DELETE FROM organizations WHERE id = $1`, orgID)
	})

	deleter := &recordingDeleter{}
	n, err := repo.PurgeDeletedTickets(ctx, time.Now().Add(-30*24*time.Hour), 100, deleter)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if n != 1 {
		t.Fatalf("purged=%d want 1", n)
	}
	if len(deleter.keys) != 2 {
		t.Fatalf("deleted objects=%v want original+thumbnail", deleter.keys)
	}

	for _, table := range []string{
		"comments", "ticket_events", "attachments", "notifications",
		"ticket_ratings", "satisfaction_ratings", "inbound_emails",
	} {
		var count int
		q := "SELECT count(*) FROM " + table + " WHERE org_id = $1"
		if err := pool.QueryRow(ctx, q, orgID).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("%s rows remain: %d", table, count)
		}
	}

	var oldCount, recentCount int
	if err := pool.QueryRow(ctx,
		`SELECT
		   count(*) FILTER (WHERE id = $2),
		   count(*) FILTER (WHERE id = $3)
		 FROM tickets WHERE org_id = $1`,
		orgID, oldTicket, recentTicket,
	).Scan(&oldCount, &recentCount); err != nil {
		t.Fatalf("count tickets: %v", err)
	}
	if oldCount != 0 {
		t.Fatal("old ticket was not hard-deleted")
	}
	if recentCount != 1 {
		t.Fatal("recently deleted ticket was hard-deleted too early")
	}
}

func TestPurgeDeletedTicketsStopsOnObjectFailure(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	repo := NewRepository(pool)

	orgID := uuid.New()
	customerID := uuid.New()
	ticketID := uuid.New()

	if _, err := pool.Exec(ctx,
		`INSERT INTO organizations (id, name, slug) VALUES ($1, 'Retention Failure', $2)`,
		orgID, "retention-fail-"+orgID.String()[:8],
	); err != nil {
		t.Fatalf("seed org: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO customers (id, org_id, email) VALUES ($1, $2, $3)`,
		customerID, orgID, "retention-fail-"+customerID.String()[:8]+"@test.local",
	); err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO tickets (id, org_id, number, requester_id, subject, status, priority, channel, deleted_at)
		 VALUES ($1, $2, 1, $3, 'Old', 'open', 'normal', 'api', now() - interval '31 days')`,
		ticketID, orgID, customerID,
	); err != nil {
		t.Fatalf("seed ticket: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO attachments (org_id, ticket_id, storage_key, filename, mime_type, size_bytes)
		 VALUES ($1, $2, 'key', 'a.pdf', 'application/pdf', 10)`,
		orgID, ticketID,
	); err != nil {
		t.Fatalf("seed attachment: %v", err)
	}

	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM attachments WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(bg, `DELETE FROM tickets WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(bg, `DELETE FROM customers WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(bg, `DELETE FROM organizations WHERE id = $1`, orgID)
	})

	deleter := &recordingDeleter{err: context.DeadlineExceeded}
	if _, err := repo.PurgeDeletedTickets(ctx, time.Now().Add(-30*24*time.Hour), 100, deleter); err == nil {
		t.Fatal("expected storage error")
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM tickets WHERE id = $1`, ticketID).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatal("ticket was deleted despite storage failure")
	}
}
