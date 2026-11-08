package notifications

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func notifyTestPool(t *testing.T) *pgxpool.Pool {
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

func seedNotifyOrg(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	var orgID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO organizations (name, slug) VALUES ('Notify Org', $1) RETURNING id`,
		"notify-"+uuid.NewString()[:8],
	).Scan(&orgID); err != nil {
		t.Fatalf("seed org: %v", err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		// Delete in FK order: children before parents. ticket_events must go
		// before tickets, and users before memberships.
		_, _ = pool.Exec(bg, `DELETE FROM notifications WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(bg, `DELETE FROM notification_preferences WHERE user_id IN (SELECT user_id FROM memberships WHERE org_id = $1)`, orgID)
		_, _ = pool.Exec(bg, `DELETE FROM ticket_watchers WHERE ticket_id IN (SELECT id FROM tickets WHERE org_id = $1)`, orgID)
		_, _ = pool.Exec(bg, `DELETE FROM ticket_tags WHERE ticket_id IN (SELECT id FROM tickets WHERE org_id = $1)`, orgID)
		_, _ = pool.Exec(bg, `DELETE FROM ticket_events WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(bg, `DELETE FROM comments WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(bg, `DELETE FROM tickets WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(bg, `DELETE FROM customers WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(bg, `DELETE FROM users WHERE id IN (SELECT user_id FROM memberships WHERE org_id = $1)`, orgID)
		_, _ = pool.Exec(bg, `DELETE FROM memberships WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(bg, `DELETE FROM organizations WHERE id = $1`, orgID)
	})
	return orgID
}

func seedNotifyUsers(t *testing.T, pool *pgxpool.Pool, orgID uuid.UUID) (requesterID, assigneeID, watcherID uuid.UUID) {
	t.Helper()
	ctx := context.Background()

	suffix := uuid.NewString()[:8]
	for i, name := range []string{"requester", "assignee", "watcher"} {
		email := name + "-" + suffix + "@notify.test"
		var id uuid.UUID
		if err := pool.QueryRow(ctx,
			`INSERT INTO users (email, password_hash, name) VALUES ($1, 'x', $2) RETURNING id`,
			email, "User "+name,
		).Scan(&id); err != nil {
			t.Fatalf("seed user %d: %v", i, err)
		}
		if _, err := pool.Exec(ctx,
			`INSERT INTO memberships (org_id, user_id, role) VALUES ($1, $2, $3)`,
			orgID, id, "agent",
		); err != nil {
			t.Fatalf("seed membership %d: %v", i, err)
		}
		switch i {
		case 0:
			requesterID = id
		case 1:
			assigneeID = id
		case 2:
			watcherID = id
		}
	}
	return
}

func seedNotifyEvent(t *testing.T, pool *pgxpool.Pool, orgID, ticketID uuid.UUID) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	var id uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO ticket_events (org_id, ticket_id, actor_type, event_type, data)
		 VALUES ($1, $2, 'system', 'ticket_assigned', '{}')
		 RETURNING id`,
		orgID, ticketID,
	).Scan(&id); err != nil {
		t.Fatalf("seed event: %v", err)
	}
	return id
}

func seedNotifyTicket(t *testing.T, pool *pgxpool.Pool, orgID, requesterID, assigneeID, watcherID uuid.UUID) (ticketID uuid.UUID, commentID uuid.UUID) {
	t.Helper()
	ctx := context.Background()

	var customerID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO customers (org_id, email) VALUES ($1, $2) RETURNING id`,
		orgID, "customer@notify.test",
	).Scan(&customerID); err != nil {
		t.Fatalf("seed customer: %v", err)
	}

	if err := pool.QueryRow(ctx,
		`INSERT INTO tickets (org_id, number, requester_id, assignee_id, subject, status, priority)
		 VALUES ($1, 1, $2, $3, 'notify ticket', 'open', 'normal')
		 RETURNING id`,
		orgID, customerID, assigneeID,
	).Scan(&ticketID); err != nil {
		t.Fatalf("seed ticket: %v", err)
	}

	if err := pool.QueryRow(ctx,
		`INSERT INTO comments (org_id, ticket_id, author_type, author_customer_id, body, is_internal)
		 VALUES ($1, $2, 'customer', $3, 'public comment', false)
		 RETURNING id`,
		orgID, ticketID, customerID,
	).Scan(&commentID); err != nil {
		t.Fatalf("seed comment: %v", err)
	}

	if _, err := pool.Exec(ctx,
		`INSERT INTO ticket_watchers (ticket_id, user_id) VALUES ($1, $2)`,
		ticketID, watcherID,
	); err != nil {
		t.Fatalf("seed watcher: %v", err)
	}

	return
}

func TestRepositoryInsertNotificationDedupe(t *testing.T) {
	pool := notifyTestPool(t)
	orgID := seedNotifyOrg(t, pool)
	requesterID, assigneeID, watcherID := seedNotifyUsers(t, pool, orgID)
	ticketID, _ := seedNotifyTicket(t, pool, orgID, requesterID, assigneeID, watcherID)

	repo := NewRepository(pool)
	eventID := seedNotifyEvent(t, pool, orgID, ticketID)
	n1, _, err := repo.InsertNotification(context.Background(), Notification{
		OrgID:    orgID,
		UserID:   assigneeID,
		EventID:  eventID,
		Type:     EventTicketAssigned,
		TicketID: &ticketID,
		Payload:  []byte(`{"ticket_number":1}`),
	})
	if err != nil {
		t.Fatalf("insert first: %v", err)
	}

	// Replay same event + user: should not create a duplicate row (ON CONFLICT DO NOTHING).
	n2, _, err := repo.InsertNotification(context.Background(), Notification{
		OrgID:    orgID,
		UserID:   assigneeID,
		EventID:  eventID,
		Type:     EventTicketAssigned,
		TicketID: &ticketID,
		Payload:  []byte(`{"ticket_number":1}`),
	})
	if err != nil {
		t.Fatalf("insert replay: %v", err)
	}
	if n2 != n1 {
		t.Fatalf("replay returned different id: %s vs %s", n2, n1)
	}

	// Different user for same event: should create a new row.
	n3, _, err := repo.InsertNotification(context.Background(), Notification{
		OrgID:    orgID,
		UserID:   requesterID,
		EventID:  eventID,
		Type:     EventTicketAssigned,
		TicketID: &ticketID,
		Payload:  []byte(`{"ticket_number":1}`),
	})
	if err != nil {
		t.Fatalf("insert different user: %v", err)
	}
	if n3 == n1 {
		t.Fatalf("different user got same id")
	}

	// Same user, different event: should create a new row.
	eventID2 := seedNotifyEvent(t, pool, orgID, ticketID)
	n4, _, err := repo.InsertNotification(context.Background(), Notification{
		OrgID:    orgID,
		UserID:   assigneeID,
		EventID:  eventID2,
		Type:     EventNewComment,
		TicketID: &ticketID,
		Payload:  []byte(`{"ticket_number":1}`),
	})
	if err != nil {
		t.Fatalf("insert different event: %v", err)
	}
	if n4 == n1 {
		t.Fatalf("different event got same id")
	}
}

func TestRepositoryMarkRead(t *testing.T) {
	pool := notifyTestPool(t)
	orgID := seedNotifyOrg(t, pool)
	requesterID, assigneeID, watcherID := seedNotifyUsers(t, pool, orgID)
	ticketID, _ := seedNotifyTicket(t, pool, orgID, requesterID, assigneeID, watcherID)

	repo := NewRepository(pool)
	eventID := seedNotifyEvent(t, pool, orgID, ticketID)
	nID, _, err := repo.InsertNotification(context.Background(), Notification{
		OrgID:    orgID,
		UserID:   assigneeID,
		EventID:  eventID,
		Type:     EventTicketAssigned,
		TicketID: &ticketID,
		Payload:  []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Mark read.
	if err := repo.MarkRead(context.Background(), orgID, assigneeID, nID); err != nil {
		t.Fatalf("mark read: %v", err)
	}

	// Mark again: idempotent success.
	if err := repo.MarkRead(context.Background(), orgID, assigneeID, nID); err != nil {
		t.Fatalf("mark read twice: %v, want nil", err)
	}

	// Wrong user: not found.
	if err := repo.MarkRead(context.Background(), orgID, uuid.New(), nID); err != ErrNotificationNotFound {
		t.Fatalf("mark read wrong user: got %v, want ErrNotificationNotFound", err)
	}

	// Wrong org: not found even for the right user.
	if err := repo.MarkRead(context.Background(), uuid.New(), assigneeID, nID); err != ErrNotificationNotFound {
		t.Fatalf("mark read wrong org: got %v, want ErrNotificationNotFound", err)
	}
}

func TestRepositoryMarkAllRead(t *testing.T) {
	pool := notifyTestPool(t)
	orgID := seedNotifyOrg(t, pool)
	requesterID, assigneeID, watcherID := seedNotifyUsers(t, pool, orgID)
	ticketID, _ := seedNotifyTicket(t, pool, orgID, requesterID, assigneeID, watcherID)

	repo := NewRepository(pool)
	for i := 0; i < 3; i++ {
		eventID := seedNotifyEvent(t, pool, orgID, ticketID)
		if _, _, err := repo.InsertNotification(context.Background(), Notification{
			OrgID:    orgID,
			UserID:   assigneeID,
			EventID:  eventID,
			Type:     EventNewComment,
			TicketID: &ticketID,
			Payload:  []byte(`{}`),
		}); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	n, err := repo.MarkAllRead(context.Background(), orgID, assigneeID)
	if err != nil {
		t.Fatalf("mark all read: %v", err)
	}
	if n != 3 {
		t.Fatalf("marked %d, want 3", n)
	}

	// Second call: nothing to mark.
	n, err = repo.MarkAllRead(context.Background(), orgID, assigneeID)
	if err != nil {
		t.Fatalf("mark all read again: %v", err)
	}
	if n != 0 {
		t.Fatalf("marked %d on second call, want 0", n)
	}
}

func TestRepositoryListNotificationsPaginationAndUnread(t *testing.T) {
	pool := notifyTestPool(t)
	orgID := seedNotifyOrg(t, pool)
	requesterID, assigneeID, watcherID := seedNotifyUsers(t, pool, orgID)
	ticketID, _ := seedNotifyTicket(t, pool, orgID, requesterID, assigneeID, watcherID)

	repo := NewRepository(pool)
	ctx := context.Background()

	const total = 5
	for i := 0; i < total; i++ {
		eventID := seedNotifyEvent(t, pool, orgID, ticketID)
		if _, ins, err := repo.InsertNotification(ctx, Notification{
			OrgID:    orgID,
			UserID:   assigneeID,
			EventID:  eventID,
			Type:     EventNewComment,
			TicketID: &ticketID,
			Payload:  []byte(`{"i":1}`),
		}); err != nil || !ins {
			t.Fatalf("insert %d: ins=%v err=%v", i, ins, err)
		}
	}

	seen := map[uuid.UUID]bool{}
	cursor := ""
	pages := 0
	for {
		ns, next, err := repo.ListNotifications(ctx, orgID, assigneeID, false, 2, cursor)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		pages++
		for _, n := range ns {
			if seen[n.ID] {
				t.Fatalf("duplicate notification %s", n.ID)
			}
			seen[n.ID] = true
		}
		if next == "" {
			break
		}
		cursor = next
		if pages > 10 {
			t.Fatal("cursor loop")
		}
	}
	if len(seen) != total {
		t.Fatalf("listed %d notifications, want %d", len(seen), total)
	}
	if pages != 3 {
		t.Fatalf("pages=%d, want 3", pages)
	}

	// Mark one read; the unread filter must exclude it.
	if err := repo.MarkRead(ctx, orgID, assigneeID, firstID(seen)); err != nil {
		t.Fatalf("mark read: %v", err)
	}
	unread, _, err := repo.ListNotifications(ctx, orgID, assigneeID, true, 100, "")
	if err != nil {
		t.Fatalf("list unread: %v", err)
	}
	if len(unread) != total-1 {
		t.Fatalf("unread=%d, want %d", len(unread), total-1)
	}
}

func firstID(seen map[uuid.UUID]bool) uuid.UUID {
	for id := range seen {
		return id
	}
	return uuid.Nil
}

func TestRepositoryListNotificationsOrgIsolation(t *testing.T) {
	pool := notifyTestPool(t)
	orgID := seedNotifyOrg(t, pool)
	otherOrgID := seedNotifyOrg(t, pool)
	reqA, userID, watcherA := seedNotifyUsers(t, pool, orgID)
	reqB, _, watcherB := seedNotifyUsers(t, pool, otherOrgID)
	ticketA, _ := seedNotifyTicket(t, pool, orgID, reqA, userID, watcherA)
	ticketB, _ := seedNotifyTicket(t, pool, otherOrgID, reqB, reqB, watcherB)

	repo := NewRepository(pool)
	ctx := context.Background()

	if _, _, err := repo.InsertNotification(ctx, Notification{
		OrgID:    orgID,
		UserID:   userID,
		EventID:  seedNotifyEvent(t, pool, orgID, ticketA),
		Type:     EventNewComment,
		TicketID: &ticketA,
		Payload:  []byte(`{}`),
	}); err != nil {
		t.Fatalf("insert own org: %v", err)
	}
	if _, _, err := repo.InsertNotification(ctx, Notification{
		OrgID:    otherOrgID,
		UserID:   userID,
		EventID:  seedNotifyEvent(t, pool, otherOrgID, ticketB),
		Type:     EventNewComment,
		TicketID: &ticketB,
		Payload:  []byte(`{}`),
	}); err != nil {
		t.Fatalf("insert other org: %v", err)
	}

	ns, _, err := repo.ListNotifications(ctx, orgID, userID, false, 100, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(ns) != 1 || ns[0].OrgID != orgID {
		t.Fatalf("got %d notifications, want only the caller's org: %+v", len(ns), ns)
	}
}

func TestRepositoryReplacePreferencesStoresOnlyExceptions(t *testing.T) {
	pool := notifyTestPool(t)
	orgID := seedNotifyOrg(t, pool)
	_, userID, _ := seedNotifyUsers(t, pool, orgID)

	repo := NewRepository(pool)
	ctx := context.Background()

	err := repo.ReplacePreferences(ctx, userID, []Preference{
		{UserID: userID, EventType: EventNewComment, Channel: ChannelInApp, Enabled: false},
		{UserID: userID, EventType: EventSLABreach, Channel: ChannelEmail, Enabled: false},
		{UserID: userID, EventType: EventTicketAssigned, Channel: ChannelInApp, Enabled: true},
	})
	if err != nil {
		t.Fatalf("replace: %v", err)
	}

	prefs, err := repo.ListPreferences(ctx, userID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(prefs) != 2 {
		t.Fatalf("stored %d preferences, want 2 disabled exceptions", len(prefs))
	}
	for _, p := range prefs {
		if p.Enabled {
			t.Fatalf("enabled preference was persisted: %+v", p)
		}
	}

	// Replacing with an empty set clears the exceptions.
	if err := repo.ReplacePreferences(ctx, userID, nil); err != nil {
		t.Fatalf("clear: %v", err)
	}
	prefs, err = repo.ListPreferences(ctx, userID)
	if err != nil {
		t.Fatalf("list after clear: %v", err)
	}
	if len(prefs) != 0 {
		t.Fatalf("stored %d preferences after clear, want 0", len(prefs))
	}
}

func TestRepositoryLoadCommentPolymorphicAuthor(t *testing.T) {
	pool := notifyTestPool(t)
	orgID := seedNotifyOrg(t, pool)
	requesterID, assigneeID, watcherID := seedNotifyUsers(t, pool, orgID)
	_, commentID := seedNotifyTicket(t, pool, orgID, requesterID, assigneeID, watcherID)

	repo := NewRepository(pool)
	c, err := repo.LoadComment(context.Background(), orgID, commentID)
	if err != nil {
		t.Fatalf("load comment: %v", err)
	}
	if c.AuthorType != "customer" || c.AuthorID == nil {
		t.Fatalf("unexpected comment author: %+v", c)
	}
	if c.IsInternal {
		t.Fatalf("comment should be public: %+v", c)
	}
}
