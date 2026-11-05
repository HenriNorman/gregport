package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/HenriNorman/gregport/internal/queue"
)

type recordingEnqueuer struct {
	mu        sync.Mutex
	jobs      []queue.Job
	err       error
	delay     time.Duration
	failAfter int
}

func (r *recordingEnqueuer) EnqueueJob(_ context.Context, job queue.Job) error {
	if r.delay > 0 {
		time.Sleep(r.delay)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	if r.failAfter > 0 && len(r.jobs) >= r.failAfter {
		return errors.New("enqueue failed after partial batch")
	}
	r.jobs = append(r.jobs, job)
	return nil
}

func (r *recordingEnqueuer) snapshot() []queue.Job {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]queue.Job, len(r.jobs))
	copy(out, r.jobs)
	return out
}

func dispatcherTestPool(t *testing.T) *pgxpool.Pool {
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

func seedOutboxTicket(t *testing.T, pool *pgxpool.Pool) (orgID, ticketID uuid.UUID) {
	t.Helper()
	ctx := context.Background()

	if err := pool.QueryRow(ctx,
		`INSERT INTO organizations (name, slug) VALUES ('Outbox Org', $1) RETURNING id`,
		"outbox-"+uuid.NewString()[:8],
	).Scan(&orgID); err != nil {
		t.Fatalf("seed org: %v", err)
	}

	var customerID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO customers (org_id, email) VALUES ($1, $2) RETURNING id`,
		orgID, "outbox-"+uuid.NewString()[:8]+"@test.local",
	).Scan(&customerID); err != nil {
		t.Fatalf("seed customer: %v", err)
	}

	if err := pool.QueryRow(ctx,
		`INSERT INTO tickets (org_id, number, requester_id, subject)
		 VALUES ($1, 1, $2, 'outbox') RETURNING id`,
		orgID, customerID,
	).Scan(&ticketID); err != nil {
		t.Fatalf("seed ticket: %v", err)
	}

	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM email_deliveries WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(bg, `DELETE FROM ticket_events WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(bg, `DELETE FROM comments WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(bg, `DELETE FROM tickets WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(bg, `DELETE FROM customers WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(bg, `DELETE FROM organizations WHERE id = $1`, orgID)
	})
	return orgID, ticketID
}

func seedEvent(t *testing.T, pool *pgxpool.Pool, orgID, ticketID uuid.UUID, eventType string, data string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO ticket_events (org_id, ticket_id, actor_type, event_type, data)
		 VALUES ($1, $2, 'system', $3, $4) RETURNING id`,
		orgID, ticketID, eventType, data,
	).Scan(&id); err != nil {
		t.Fatalf("seed event: %v", err)
	}
	return id
}

func countUnscopedEvents(t *testing.T, pool *pgxpool.Pool, orgID uuid.UUID) (total, undispatched int) {
	t.Helper()
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*), count(*) FILTER (WHERE dispatched_at IS NULL)
		 FROM ticket_events WHERE org_id = $1`,
		orgID,
	).Scan(&total, &undispatched); err != nil {
		t.Fatalf("count events: %v", err)
	}
	return total, undispatched
}

func TestDispatchOnceEnqueuesOneJobAndMarksDispatched(t *testing.T) {
	pool := dispatcherTestPool(t)
	orgID, ticketID := seedOutboxTicket(t, pool)
	eventID := seedEvent(t, pool, orgID, ticketID, "ticket.created", `{"number":1,"priority":"normal"}`)

	enqueuer := &recordingEnqueuer{}
	d := NewDispatcher(pool, enqueuer, nil, 100, time.Second)

	n, err := d.DispatchOnce(context.Background())
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if n != 1 {
		t.Fatalf("dispatched events: %d, want 1", n)
	}

	jobs := enqueuer.snapshot()
	if len(jobs) != 1 {
		t.Fatalf("jobs: %d, want 1", len(jobs))
	}
	job := jobs[0]
	if job.ID != eventID.String()+":email" {
		t.Fatalf("job id: %q", job.ID)
	}
	if job.Type != queue.TaskEmailSend || job.Queue != queue.QueueCritical {
		t.Fatalf("job routing: %+v", job)
	}
	if job.MaxRetry != 10 {
		t.Fatalf("max retry: %d", job.MaxRetry)
	}

	var payload EventJob
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if payload.EventID != eventID || payload.OrgID != orgID || payload.TicketID != ticketID {
		t.Fatalf("payload ids: %+v", payload)
	}
	if payload.EventType != "ticket.created" {
		t.Fatalf("payload type: %q", payload.EventType)
	}
	var data map[string]any
	if err := json.Unmarshal(payload.Data, &data); err != nil {
		t.Fatalf("payload data: %v", err)
	}
	if data["priority"] != "normal" {
		t.Fatalf("payload data: %v", data)
	}

	_, undispatched := countUnscopedEvents(t, pool, orgID)
	if undispatched != 0 {
		t.Fatalf("undispatched: %d, want 0", undispatched)
	}

	// A second poll must not enqueue the same event again.
	n, err = d.DispatchOnce(context.Background())
	if err != nil {
		t.Fatalf("second dispatch: %v", err)
	}
	if n != 0 || len(enqueuer.snapshot()) != 1 {
		t.Fatalf("second dispatch re-enqueued: events=%d jobs=%d", n, len(enqueuer.snapshot()))
	}
}

func TestTwoDispatchersDispatchEachEventExactlyOnce(t *testing.T) {
	pool := dispatcherTestPool(t)
	orgID, ticketID := seedOutboxTicket(t, pool)

	const eventCount = 150
	for i := 0; i < eventCount; i++ {
		seedEvent(t, pool, orgID, ticketID, "ticket.created", `{"number":1}`)
	}

	// A small batch and a short delay create real overlap between the two
	// pollers, exercising FOR UPDATE SKIP LOCKED.
	enqueuer := &recordingEnqueuer{delay: 2 * time.Millisecond}
	d1 := NewDispatcher(pool, enqueuer, nil, 20, time.Second)
	d2 := NewDispatcher(pool, enqueuer, nil, 20, time.Second)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, errs[0] = d1.DispatchOnce(context.Background())
	}()
	go func() {
		defer wg.Done()
		_, errs[1] = d2.DispatchOnce(context.Background())
	}()
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("dispatcher %d: %v", i, err)
		}
	}

	jobs := enqueuer.snapshot()
	if len(jobs) != eventCount {
		t.Fatalf("jobs: %d, want %d", len(jobs), eventCount)
	}

	seen := make(map[string]bool, len(jobs))
	for _, job := range jobs {
		if seen[job.ID] {
			t.Fatalf("duplicate job id: %s", job.ID)
		}
		seen[job.ID] = true
	}

	_, undispatched := countUnscopedEvents(t, pool, orgID)
	if undispatched != 0 {
		t.Fatalf("undispatched: %d, want 0", undispatched)
	}
}

func TestEnqueueFailureLeavesEventsUndispatched(t *testing.T) {
	pool := dispatcherTestPool(t)
	orgID, ticketID := seedOutboxTicket(t, pool)
	seedEvent(t, pool, orgID, ticketID, "ticket.created", `{}`)

	enqueuer := &recordingEnqueuer{err: errors.New("redis down")}
	d := NewDispatcher(pool, enqueuer, nil, 100, time.Second)

	if _, err := d.DispatchOnce(context.Background()); err == nil {
		t.Fatal("expected dispatch error")
	}

	_, undispatched := countUnscopedEvents(t, pool, orgID)
	if undispatched != 1 {
		t.Fatalf("undispatched: %d, want 1 (transaction must roll back)", undispatched)
	}
	if len(enqueuer.snapshot()) != 0 {
		t.Fatal("failed enqueue must not record a job")
	}
}

func TestPartialBatchFailureLeavesWholeBatchUndispatched(t *testing.T) {
	pool := dispatcherTestPool(t)
	orgID, ticketID := seedOutboxTicket(t, pool)
	for i := 0; i < 3; i++ {
		seedEvent(t, pool, orgID, ticketID, "ticket.created", `{}`)
	}

	// The first job succeeds, the second fails. The transaction rolls back,
	// so all three events stay undispatched even though one job is already in
	// the queue; consumers deduplicate that retry.
	enqueuer := &recordingEnqueuer{failAfter: 1}
	d := NewDispatcher(pool, enqueuer, nil, 100, time.Second)

	if _, err := d.DispatchOnce(context.Background()); err == nil {
		t.Fatal("expected dispatch error")
	}

	_, undispatched := countUnscopedEvents(t, pool, orgID)
	if undispatched != 3 {
		t.Fatalf("undispatched: %d, want 3", undispatched)
	}
	if got := len(enqueuer.snapshot()); got != 1 {
		t.Fatalf("enqueued before failure: %d, want 1", got)
	}
}

func TestRunDispatchesPendingEvents(t *testing.T) {
	pool := dispatcherTestPool(t)
	orgID, ticketID := seedOutboxTicket(t, pool)
	seedEvent(t, pool, orgID, ticketID, "ticket.created", `{}`)

	enqueuer := &recordingEnqueuer{}
	d := NewDispatcher(pool, enqueuer, nil, 100, 5*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		d.Run(ctx)
		close(done)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for len(enqueuer.snapshot()) == 0 {
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatal("Run did not dispatch the pending event")
		}
		time.Sleep(5 * time.Millisecond)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after cancelling")
	}

	_, undispatched := countUnscopedEvents(t, pool, orgID)
	if undispatched != 0 {
		t.Fatalf("undispatched: %d, want 0", undispatched)
	}
}

func TestUnknownEventTypeIsMarkedDispatchedWithoutJobs(t *testing.T) {
	pool := dispatcherTestPool(t)
	orgID, ticketID := seedOutboxTicket(t, pool)
	seedEvent(t, pool, orgID, ticketID, "ticket.updated", `{}`)

	enqueuer := &recordingEnqueuer{}
	d := NewDispatcher(pool, enqueuer, nil, 100, time.Second)

	n, err := d.DispatchOnce(context.Background())
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if n != 1 {
		t.Fatalf("events: %d, want 1", n)
	}
	if len(enqueuer.snapshot()) != 0 {
		t.Fatal("unknown event must not enqueue jobs")
	}
	_, undispatched := countUnscopedEvents(t, pool, orgID)
	if undispatched != 0 {
		t.Fatalf("unknown event should still be marked dispatched, got %d undispatched", undispatched)
	}
}

func TestConsumersFor(t *testing.T) {
	for _, eventType := range []string{"ticket.created", "comment.added", "ticket.assigned", "sla.breached"} {
		cs := ConsumersFor(eventType)
		if len(cs) != 1 || cs[0].Name != ConsumerEmail || cs[0].TaskType != queue.TaskEmailSend {
			t.Fatalf("%s consumers: %+v", eventType, cs)
		}
	}
	if cs := ConsumersFor("ticket.updated"); len(cs) != 0 {
		t.Fatalf("unexpected consumers: %+v", cs)
	}
}

func TestDispatcherDefaults(t *testing.T) {
	d := NewDispatcher(nil, &recordingEnqueuer{}, nil, 0, 0)
	if d.batch != 100 {
		t.Fatalf("batch: %d", d.batch)
	}
	if d.interval != time.Second {
		t.Fatalf("interval: %s", d.interval)
	}
	if d.logger == nil {
		t.Fatal("logger must default")
	}
}

func TestRunStopsOnContextCancellation(t *testing.T) {
	pool := dispatcherTestPool(t)
	_, ticketID := seedOutboxTicket(t, pool)
	_ = ticketID

	enqueuer := &recordingEnqueuer{delay: time.Millisecond}
	d := NewDispatcher(pool, enqueuer, nil, 100, 5*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		d.Run(ctx)
		close(done)
	}()

	time.Sleep(30 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop on cancellation")
	}
}
