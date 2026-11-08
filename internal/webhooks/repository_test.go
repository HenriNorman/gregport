package webhooks

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
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

type repoFixture struct {
	orgID      uuid.UUID
	customerID uuid.UUID
	ticketID   uuid.UUID
	adminID    uuid.UUID
}

func seedRepoFixture(t *testing.T, pool *pgxpool.Pool) repoFixture {
	t.Helper()
	ctx := context.Background()

	var f repoFixture
	if err := pool.QueryRow(ctx,
		`INSERT INTO organizations (name, slug) VALUES ('Webhook Org', $1) RETURNING id`,
		"webhook-"+uuid.NewString()[:8],
	).Scan(&f.orgID); err != nil {
		t.Fatalf("seed org: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO customers (org_id, email, name)
		 VALUES ($1, $2, 'Hook Customer') RETURNING id`,
		f.orgID, uuid.NewString()+"@example.test",
	).Scan(&f.customerID); err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO tickets (org_id, number, requester_id, subject, status, priority)
		 VALUES ($1, $2, $3, 'Webhook subject', 'open', 'high') RETURNING id`,
		f.orgID, 2_000_000+time.Now().UnixNano()%100_000, f.customerID,
	).Scan(&f.ticketID); err != nil {
		t.Fatalf("seed ticket: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (email, password_hash, name)
		 VALUES ($1, 'x', 'Hook Admin') RETURNING id`,
		"hook-admin-"+uuid.NewString()[:8]+"@test.local",
	).Scan(&f.adminID); err != nil {
		t.Fatalf("seed admin: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO memberships (org_id, user_id, role) VALUES ($1, $2, 'admin')`,
		f.orgID, f.adminID,
	); err != nil {
		t.Fatalf("seed membership: %v", err)
	}

	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM notifications WHERE org_id = $1`, f.orgID)
		_, _ = pool.Exec(bg, `DELETE FROM webhook_deliveries WHERE org_id = $1`, f.orgID)
		_, _ = pool.Exec(bg, `DELETE FROM webhook_subscriptions WHERE org_id = $1`, f.orgID)
		_, _ = pool.Exec(bg, `DELETE FROM ticket_events WHERE org_id = $1`, f.orgID)
		_, _ = pool.Exec(bg, `DELETE FROM tickets WHERE org_id = $1`, f.orgID)
		_, _ = pool.Exec(bg, `DELETE FROM customers WHERE org_id = $1`, f.orgID)
		_, _ = pool.Exec(bg, `DELETE FROM memberships WHERE user_id = $1`, f.adminID)
		_, _ = pool.Exec(bg, `DELETE FROM users WHERE id = $1`, f.adminID)
		_, _ = pool.Exec(bg, `DELETE FROM organizations WHERE id = $1`, f.orgID)
	})
	return f
}

func seedEvent(t *testing.T, pool *pgxpool.Pool, f repoFixture, eventType string) uuid.UUID {
	t.Helper()
	var eventID uuid.UUID
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO ticket_events (org_id, ticket_id, actor_type, event_type, data)
		 VALUES ($1, $2, 'system', $3, '{}'::jsonb) RETURNING id`,
		f.orgID, f.ticketID, eventType,
	).Scan(&eventID); err != nil {
		t.Fatalf("seed event: %v", err)
	}
	return eventID
}

func createRepoSub(t *testing.T, repo *Repository, orgID uuid.UUID, events []string, active bool) Subscription {
	t.Helper()
	sub, err := repo.Create(context.Background(), orgID, Subscription{
		URL:    "https://example.test/hooks",
		Secret: "whsec_test_secret",
		Events: events,
	})
	if err != nil {
		t.Fatalf("create subscription: %v", err)
	}
	if !active {
		sub.IsActive = false
		updated, err := repo.Update(context.Background(), orgID, sub, false)
		if err != nil {
			t.Fatalf("deactivate subscription: %v", err)
		}
		sub = updated
	}
	return sub
}

func TestRepositorySubscriptionTenantIsolation(t *testing.T) {
	pool := testPool(t)
	repo := NewRepository(pool)
	ctx := context.Background()

	a := seedRepoFixture(t, pool)
	b := seedRepoFixture(t, pool)
	sub := createRepoSub(t, repo, a.orgID, []string{EventTicketCreated}, true)

	if _, err := repo.Get(ctx, b.orgID, sub.ID); !errors.Is(err, ErrWebhookNotFound) {
		t.Fatalf("cross-tenant get err=%v", err)
	}
	if _, err := repo.Update(ctx, b.orgID, sub, false); !errors.Is(err, ErrWebhookNotFound) {
		t.Fatalf("cross-tenant update err=%v", err)
	}
	if err := repo.Delete(ctx, b.orgID, sub.ID); !errors.Is(err, ErrWebhookNotFound) {
		t.Fatalf("cross-tenant delete err=%v", err)
	}

	listA, err := repo.List(ctx, a.orgID)
	if err != nil || len(listA) != 1 || listA[0].ID != sub.ID {
		t.Fatalf("list A: %+v err=%v", listA, err)
	}
	listB, err := repo.List(ctx, b.orgID)
	if err != nil || len(listB) != 0 {
		t.Fatalf("list B: %+v err=%v", listB, err)
	}

	if err := repo.Delete(ctx, a.orgID, sub.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := repo.Get(ctx, a.orgID, sub.ID); !errors.Is(err, ErrWebhookNotFound) {
		t.Fatalf("deleted get err=%v", err)
	}
}

func TestRepositoryListActiveForEvent(t *testing.T) {
	pool := testPool(t)
	repo := NewRepository(pool)
	ctx := context.Background()
	f := seedRepoFixture(t, pool)

	match := createRepoSub(t, repo, f.orgID, []string{EventTicketCreated, EventCommentAdded}, true)
	createRepoSub(t, repo, f.orgID, []string{EventCommentAdded}, true)
	createRepoSub(t, repo, f.orgID, []string{EventTicketCreated}, false)

	subs, err := repo.ListActiveForEvent(ctx, f.orgID, EventTicketCreated)
	if err != nil {
		t.Fatalf("list active: %v", err)
	}
	if len(subs) != 1 || subs[0].ID != match.ID {
		t.Fatalf("subs: %+v", subs)
	}

	// Exact JSONB membership: a prefix or superstring must not match.
	if subs, err := repo.ListActiveForEvent(ctx, f.orgID, "ticket"); err != nil || len(subs) != 0 {
		t.Fatalf("prefix match: %+v err=%v", subs, err)
	}
	if subs, err := repo.ListActiveForEvent(ctx, f.orgID, "ticket.created.extra"); err != nil || len(subs) != 0 {
		t.Fatalf("superstring match: %+v err=%v", subs, err)
	}
}

func TestRepositoryInsertDeliveryIsIdempotent(t *testing.T) {
	pool := testPool(t)
	repo := NewRepository(pool)
	ctx := context.Background()
	f := seedRepoFixture(t, pool)
	sub := createRepoSub(t, repo, f.orgID, []string{EventTicketCreated}, true)
	eventID := seedEvent(t, pool, f, EventTicketCreated)

	first, inserted, err := repo.InsertDelivery(ctx, Delivery{
		OrgID:          f.orgID,
		SubscriptionID: sub.ID,
		EventID:        &eventID,
		EventType:      EventTicketCreated,
		Payload:        []byte(`{"id":"one"}`),
	})
	if err != nil || !inserted {
		t.Fatalf("first insert: %+v inserted=%v err=%v", first, inserted, err)
	}
	if first.Status != StatusPending || first.Attempts != 0 || first.EventID == nil || *first.EventID != eventID {
		t.Fatalf("first: %+v", first)
	}

	second, inserted, err := repo.InsertDelivery(ctx, Delivery{
		OrgID:          f.orgID,
		SubscriptionID: sub.ID,
		EventID:        &eventID,
		EventType:      EventTicketCreated,
		Payload:        []byte(`{"id":"two"}`),
	})
	if err != nil {
		t.Fatalf("second insert: %v", err)
	}
	if inserted {
		t.Fatal("duplicate insert reported inserted=true")
	}
	if second.ID != first.ID {
		t.Fatalf("duplicate id=%s want=%s", second.ID, first.ID)
	}
	var payload map[string]string
	if err := json.Unmarshal(second.Payload, &payload); err != nil {
		t.Fatalf("duplicate payload: %v", err)
	}
	if payload["id"] != "one" {
		t.Fatalf("duplicate payload overwritten: %s", second.Payload)
	}

	var count int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM webhook_deliveries WHERE subscription_id = $1`, sub.ID,
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("deliveries=%d", count)
	}

	// Delete cascades the delivery log.
	if err := repo.Delete(ctx, f.orgID, sub.ID); err != nil {
		t.Fatalf("delete subscription: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM webhook_deliveries WHERE subscription_id = $1`, sub.ID,
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("cascade left %d deliveries", count)
	}
}

func TestRepositoryDeliveryPagination(t *testing.T) {
	pool := testPool(t)
	repo := NewRepository(pool)
	ctx := context.Background()
	f := seedRepoFixture(t, pool)
	sub := createRepoSub(t, repo, f.orgID, []string{EventTicketCreated}, true)

	var ids []uuid.UUID
	for i := 0; i < 5; i++ {
		d, _, err := repo.InsertDelivery(ctx, Delivery{
			OrgID:          f.orgID,
			SubscriptionID: sub.ID,
			EventType:      EventPing,
			Payload:        []byte(`{}`),
		})
		if err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
		ids = append(ids, d.ID)
	}
	// Distinct created_at values make the keyset order deterministic.
	for i, id := range ids {
		if _, err := pool.Exec(ctx,
			`UPDATE webhook_deliveries SET created_at = now() - make_interval(secs => $2) WHERE id = $1`,
			id, 10-i,
		); err != nil {
			t.Fatal(err)
		}
	}

	page1, cursor, err := repo.ListDeliveries(ctx, f.orgID, sub.ID, 2, "")
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if len(page1) != 2 || cursor == "" {
		t.Fatalf("page1 len=%d cursor=%q", len(page1), cursor)
	}
	if page1[0].ID != ids[4] || page1[1].ID != ids[3] {
		t.Fatalf("page1 order: %+v", page1)
	}

	page2, cursor2, err := repo.ListDeliveries(ctx, f.orgID, sub.ID, 2, cursor)
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	if len(page2) != 2 || cursor2 == "" {
		t.Fatalf("page2 len=%d cursor=%q", len(page2), cursor2)
	}
	if page2[0].ID != ids[2] || page2[1].ID != ids[1] {
		t.Fatalf("page2 order: %+v", page2)
	}

	page3, cursor3, err := repo.ListDeliveries(ctx, f.orgID, sub.ID, 2, cursor2)
	if err != nil {
		t.Fatalf("page3: %v", err)
	}
	if len(page3) != 1 || cursor3 != "" || page3[0].ID != ids[0] {
		t.Fatalf("page3: %+v cursor=%q", page3, cursor3)
	}

	if _, _, err := repo.ListDeliveries(ctx, f.orgID, sub.ID, 2, "not-a-cursor"); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("bad cursor err=%v", err)
	}

	// Another org cannot page this subscription's log.
	other := seedRepoFixture(t, pool)
	if page, _, err := repo.ListDeliveries(ctx, other.orgID, sub.ID, 10, ""); err != nil || len(page) != 0 {
		t.Fatalf("cross-tenant deliveries: %+v err=%v", page, err)
	}
}

func TestRepositorySuccessResetsFailures(t *testing.T) {
	pool := testPool(t)
	repo := NewRepository(pool)
	ctx := context.Background()
	f := seedRepoFixture(t, pool)
	sub := createRepoSub(t, repo, f.orgID, []string{EventTicketCreated}, true)

	d, _, err := repo.InsertDelivery(ctx, Delivery{
		OrgID: f.orgID, SubscriptionID: sub.ID, EventType: EventTicketCreated, Payload: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	d.Attempts = 1
	d.Status = StatusFailed
	if _, err := repo.RecordDeliveryFailure(ctx, d, 10, true); err != nil {
		t.Fatalf("failure: %v", err)
	}
	loaded, _, err := repo.GetDeliveryScoped(ctx, f.orgID, sub.ID, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Attempts != 1 || loaded.Status != StatusFailed {
		t.Fatalf("failed delivery: %+v", loaded)
	}
	subAfter, _ := repo.Get(ctx, f.orgID, sub.ID)
	if subAfter.ConsecutiveFailures != 1 || subAfter.LastFailureAt == nil {
		t.Fatalf("failure counter: %+v", subAfter)
	}

	status := 204
	body := "ok"
	duration := 12
	d.Attempts = 2
	d.ResponseStatus = &status
	d.ResponseBody = &body
	d.DurationMS = &duration
	if err := repo.RecordDeliverySuccess(ctx, d); err != nil {
		t.Fatalf("success: %v", err)
	}
	loaded, _, err = repo.GetDeliveryScoped(ctx, f.orgID, sub.ID, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != StatusSent || loaded.DeliveredAt == nil || loaded.NextRetryAt != nil || loaded.LastError != nil {
		t.Fatalf("sent delivery: %+v", loaded)
	}
	if loaded.ResponseStatus == nil || *loaded.ResponseStatus != 204 {
		t.Fatalf("response status: %+v", loaded.ResponseStatus)
	}
	if loaded.DurationMS == nil || *loaded.DurationMS != 12 {
		t.Fatalf("duration: %+v", loaded.DurationMS)
	}
	subAfter, _ = repo.Get(ctx, f.orgID, sub.ID)
	if subAfter.ConsecutiveFailures != 0 || subAfter.LastFailureAt != nil {
		t.Fatalf("failures not reset: %+v", subAfter)
	}
}

func TestRepositoryFailureAutoDisablesAndNotifiesAdmins(t *testing.T) {
	pool := testPool(t)
	repo := NewRepository(pool)
	ctx := context.Background()
	f := seedRepoFixture(t, pool)
	sub := createRepoSub(t, repo, f.orgID, []string{EventTicketCreated}, true)

	d, _, err := repo.InsertDelivery(ctx, Delivery{
		OrgID: f.orgID, SubscriptionID: sub.ID, EventType: EventTicketCreated, Payload: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}

	for attempt := 1; attempt <= 2; attempt++ {
		d.Attempts = attempt
		d.Status = StatusFailed
		disabled, err := repo.RecordDeliveryFailure(ctx, d, 2, true)
		if err != nil {
			t.Fatalf("failure %d: %v", attempt, err)
		}
		if attempt == 1 && disabled {
			t.Fatal("disabled after the first failure")
		}
		if attempt == 2 && !disabled {
			t.Fatal("not disabled after the threshold")
		}
	}

	subAfter, err := repo.Get(ctx, f.orgID, sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	if subAfter.IsActive || subAfter.DisabledAt == nil || subAfter.ConsecutiveFailures != 2 {
		t.Fatalf("subscription: %+v", subAfter)
	}

	var count int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM notifications
		 WHERE org_id = $1 AND user_id = $2 AND type = 'webhook_disabled'
		   AND payload->>'webhook_id' = $3`,
		f.orgID, f.adminID, sub.ID.String(),
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("admin notifications=%d", count)
	}

	// The counter stops at the disable threshold: later attempts for the same
	// subscription do not increment or notify again.
	more, _, err := repo.InsertDelivery(ctx, Delivery{
		OrgID: f.orgID, SubscriptionID: sub.ID, EventType: EventTicketCreated, Payload: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	more.Attempts = 1
	more.Status = StatusFailed
	if disabled, err := repo.RecordDeliveryFailure(ctx, more, 2, true); err != nil || disabled {
		t.Fatalf("post-disable failure disabled=%v err=%v", disabled, err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM notifications
		 WHERE org_id = $1 AND user_id = $2 AND type = 'webhook_disabled'`,
		f.orgID, f.adminID,
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("duplicate notifications=%d", count)
	}
	subAfter, _ = repo.Get(ctx, f.orgID, sub.ID)
	if subAfter.ConsecutiveFailures != 2 {
		t.Fatalf("counter moved while inactive: %d", subAfter.ConsecutiveFailures)
	}

	// Manual re-enable resets the breaker.
	subAfter.IsActive = true
	reenabled, err := repo.Update(ctx, f.orgID, subAfter, true)
	if err != nil {
		t.Fatalf("re-enable: %v", err)
	}
	if !reenabled.IsActive || reenabled.ConsecutiveFailures != 0 ||
		reenabled.DisabledAt != nil || reenabled.LastFailureAt != nil {
		t.Fatalf("re-enabled: %+v", reenabled)
	}
}

func TestRepositoryFailureWithoutCountLeavesBreaker(t *testing.T) {
	pool := testPool(t)
	repo := NewRepository(pool)
	ctx := context.Background()
	f := seedRepoFixture(t, pool)
	sub := createRepoSub(t, repo, f.orgID, []string{EventTicketCreated}, true)

	d, _, err := repo.InsertDelivery(ctx, Delivery{
		OrgID: f.orgID, SubscriptionID: sub.ID, EventType: EventPing, Payload: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	d.Attempts = 1
	d.Status = StatusFailed
	disabled, err := repo.RecordDeliveryFailure(ctx, d, 1, false)
	if err != nil || disabled {
		t.Fatalf("disabled=%v err=%v", disabled, err)
	}
	subAfter, _ := repo.Get(ctx, f.orgID, sub.ID)
	if !subAfter.IsActive || subAfter.ConsecutiveFailures != 0 {
		t.Fatalf("manual attempt changed breaker: %+v", subAfter)
	}
	loaded, _, _ := repo.GetDeliveryScoped(ctx, f.orgID, sub.ID, d.ID)
	if loaded.Status != StatusFailed || loaded.Attempts != 1 {
		t.Fatalf("delivery: %+v", loaded)
	}
}

func TestRepositoryResetDeliveryForRedelivery(t *testing.T) {
	pool := testPool(t)
	repo := NewRepository(pool)
	ctx := context.Background()
	f := seedRepoFixture(t, pool)
	sub := createRepoSub(t, repo, f.orgID, []string{EventTicketCreated}, true)

	d, _, err := repo.InsertDelivery(ctx, Delivery{
		OrgID: f.orgID, SubscriptionID: sub.ID, EventType: EventTicketCreated, Payload: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	status := 500
	body := "boom"
	errText := "HTTP 500"
	next := time.Now().Add(time.Minute)
	d.Status = StatusDead
	d.Attempts = 7
	d.ResponseStatus = &status
	d.ResponseBody = &body
	d.LastError = &errText
	d.NextRetryAt = &next
	if _, err := repo.RecordDeliveryFailure(ctx, d, 10, false); err != nil {
		t.Fatalf("record failure: %v", err)
	}

	reset, gotSub, err := repo.ResetDeliveryForRedelivery(ctx, f.orgID, sub.ID, d.ID)
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if gotSub.ID != sub.ID {
		t.Fatalf("subscription: %+v", gotSub)
	}
	if reset.Status != StatusPending || reset.Attempts != 0 ||
		reset.ResponseStatus != nil || reset.ResponseBody != nil ||
		reset.LastError != nil || reset.NextRetryAt != nil || reset.DeliveredAt != nil {
		t.Fatalf("reset delivery: %+v", reset)
	}

	if _, _, err := repo.ResetDeliveryForRedelivery(ctx, f.orgID, sub.ID, uuid.New()); !errors.Is(err, ErrDeliveryNotFound) {
		t.Fatalf("missing delivery err=%v", err)
	}
	other := seedRepoFixture(t, pool)
	if _, _, err := repo.ResetDeliveryForRedelivery(ctx, other.orgID, sub.ID, d.ID); !errors.Is(err, ErrDeliveryNotFound) {
		t.Fatalf("cross-tenant reset err=%v", err)
	}
}

func TestRepositoryLoadTicketSummary(t *testing.T) {
	pool := testPool(t)
	repo := NewRepository(pool)
	ctx := context.Background()
	f := seedRepoFixture(t, pool)

	summary, err := repo.LoadTicketSummary(ctx, f.orgID, f.ticketID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if summary.ID != f.ticketID || summary.Subject != "Webhook subject" ||
		summary.Status != "open" || summary.Priority != "high" || summary.RequesterID != f.customerID {
		t.Fatalf("summary: %+v", summary)
	}
	if summary.AssigneeID != nil {
		t.Fatalf("assignee: %v", summary.AssigneeID)
	}

	if _, err := repo.LoadTicketSummary(ctx, uuid.New(), f.ticketID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant err=%v", err)
	}

	// A soft-deleted ticket still has a summary so ticket.deleted can carry
	// context.
	if _, err := pool.Exec(ctx, `UPDATE tickets SET deleted_at = now() WHERE id = $1`, f.ticketID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.LoadTicketSummary(ctx, f.orgID, f.ticketID); err != nil {
		t.Fatalf("deleted ticket summary: %v", err)
	}
}

func TestRepositoryLoadDeliveryScoping(t *testing.T) {
	pool := testPool(t)
	repo := NewRepository(pool)
	ctx := context.Background()
	f := seedRepoFixture(t, pool)
	sub := createRepoSub(t, repo, f.orgID, []string{EventTicketCreated}, true)
	other := seedRepoFixture(t, pool)

	d, _, err := repo.InsertDelivery(ctx, Delivery{
		OrgID: f.orgID, SubscriptionID: sub.ID, EventType: EventTicketCreated, Payload: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}

	for attempt, args := range [][3]uuid.UUID{
		{f.orgID, sub.ID, d.ID},
		{other.orgID, sub.ID, d.ID},
		{f.orgID, uuid.New(), d.ID},
		{f.orgID, sub.ID, uuid.New()},
	} {
		_, _, err := repo.GetDeliveryScoped(ctx, args[0], args[1], args[2])
		if attempt == 0 {
			if err != nil {
				t.Fatalf("scoped load: %v", err)
			}
			continue
		}
		if !errors.Is(err, ErrDeliveryGone) {
			t.Fatalf("case %d err=%v, want ErrDeliveryGone", attempt, err)
		}
	}

	loaded, gotSub, err := repo.LoadDeliveryForAttempt(ctx, d.ID)
	if err != nil {
		t.Fatalf("load for attempt: %v", err)
	}
	if loaded.ID != d.ID || gotSub.Secret != "whsec_test_secret" || gotSub.URL != "https://example.test/hooks" {
		t.Fatalf("load for attempt: %+v %+v", loaded, gotSub)
	}
	if _, _, err := repo.LoadDeliveryForAttempt(ctx, uuid.New()); !errors.Is(err, ErrDeliveryGone) {
		t.Fatalf("missing attempt err=%v", err)
	}
}
