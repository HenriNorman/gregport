package outbox

import (
	"context"
	"io"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/HenriNorman/gregport/internal/email"
	"github.com/HenriNorman/gregport/internal/platform/cache"
	"github.com/HenriNorman/gregport/internal/platform/requestctx"
	"github.com/HenriNorman/gregport/internal/queue"
	"github.com/HenriNorman/gregport/internal/ratings"
	"github.com/HenriNorman/gregport/internal/sla"
	"github.com/HenriNorman/gregport/internal/tickets"
	"github.com/HenriNorman/gregport/internal/worker"
)

type pipelineEmail struct {
	to       string
	template string
	payload  email.DeliveryPayload
}

type pipelineSender struct {
	mu    sync.Mutex
	calls []pipelineEmail
}

func (s *pipelineSender) Send(_ context.Context, to, template string, payload email.DeliveryPayload) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, pipelineEmail{to: to, template: template, payload: payload})
	return nil
}

func (s *pipelineSender) snapshot() []pipelineEmail {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]pipelineEmail, len(s.calls))
	copy(out, s.calls)
	return out
}

// pipelineFixture seeds an org, an SLA policy and an agent, then creates a
// ticket through the ticket service so the real SLA and outbox paths run.
type pipelineFixture struct {
	orgID          uuid.UUID
	ticketID       uuid.UUID
	supportEmail   string
	requesterEmail string
	repo           *tickets.Repository
}

func seedPipelineFixture(t *testing.T, pool *pgxpool.Pool) pipelineFixture {
	t.Helper()
	ctx := context.Background()

	var orgID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO organizations (name, slug, settings)
		 VALUES ('Pipeline Org', $1, '{"support_email":"support@pipeline.test"}')
		 RETURNING id`,
		"pipeline-"+uuid.NewString()[:8],
	).Scan(&orgID); err != nil {
		t.Fatalf("seed org: %v", err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM email_deliveries WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(bg, `DELETE FROM ticket_events WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(bg, `DELETE FROM comments WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(bg, `DELETE FROM tickets WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(bg, `DELETE FROM customers WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(bg, `DELETE FROM sla_policies WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(bg, `DELETE FROM organizations WHERE id = $1`, orgID)
	})

	slaRepo := sla.NewRepository(pool)
	if _, err := slaRepo.Create(ctx, orgID, sla.Policy{
		Name:                 "Urgent default",
		Priority:             "urgent",
		FirstResponseMinutes: 30,
		ResolutionMinutes:    240,
	}); err != nil {
		t.Fatalf("seed SLA policy: %v", err)
	}

	repo := tickets.NewRepository(pool)
	svc := tickets.NewService(repo, tickets.NewInvitationTokenManager(time.Hour))
	svc.SetSLAStore(sla.NewCachedStore(slaRepo, cache.NewMemory(), time.Minute))

	requesterEmail := "requester-" + uuid.NewString()[:8] + "@pipeline.test"
	tk, err := svc.CreateTicket(ctx, requestctx.Principal{
		Kind:    requestctx.KindUser,
		ActorID: uuid.New(),
		OrgID:   orgID,
		Role:    "owner",
	}, tickets.CreateTicketRequest{
		RequesterEmail: requesterEmail,
		RequesterName:  "Pipeline Requester",
		Subject:        "End-to-end pipeline",
		Body:           "Please help.",
		Priority:       "urgent",
	})
	if err != nil {
		t.Fatalf("create ticket: %v", err)
	}
	if tk.FirstResponseDueAt == nil || tk.ResolutionDueAt == nil {
		t.Fatalf("ticket was created without SLA due dates: %+v", tk)
	}

	return pipelineFixture{
		orgID:          orgID,
		ticketID:       tk.ID,
		supportEmail:   "support@pipeline.test",
		requesterEmail: requesterEmail,
		repo:           repo,
	}
}

func loadPipelineEvent(t *testing.T, pool *pgxpool.Pool, ticketID uuid.UUID, eventType string) Event {
	t.Helper()
	var ev Event
	if err := pool.QueryRow(context.Background(),
		`SELECT id, org_id, ticket_id, event_type, data, created_at
		 FROM ticket_events
		 WHERE ticket_id = $1 AND event_type = $2
		 ORDER BY created_at DESC, id DESC
		 LIMIT 1`,
		ticketID, eventType,
	).Scan(&ev.ID, &ev.OrgID, &ev.TicketID, &ev.EventType, &ev.Data, &ev.CreatedAt); err != nil {
		t.Fatalf("load %s event: %v", eventType, err)
	}
	return ev
}

func enqueuePipelineEvent(t *testing.T, pool *pgxpool.Pool, client *queue.Client, ev Event) queue.Job {
	t.Helper()
	consumers := ConsumersFor(ev)
	if len(consumers) == 0 {
		t.Fatalf("event %s has no consumers", ev.EventType)
	}
	// Enqueue the email consumer (index 0). The notify consumer is exercised
	// by the dedicated notify package tests.
	job, err := jobFor(ev, consumers[0])
	if err != nil {
		t.Fatalf("job for %s: %v", ev.EventType, err)
	}
	if err := client.EnqueueJob(context.Background(), job); err != nil {
		t.Fatalf("enqueue %s: %v", ev.EventType, err)
	}
	// The dispatcher marks the row in the same transaction as the enqueue.
	// This test enqueues directly, so mark the single row it exercised.
	if _, err := pool.Exec(context.Background(),
		`UPDATE ticket_events SET dispatched_at = now() WHERE id = $1`, ev.ID,
	); err != nil {
		t.Fatalf("mark %s dispatched: %v", ev.EventType, err)
	}
	return job
}

func waitForSentDelivery(t *testing.T, pool *pgxpool.Pool, orgID uuid.UUID, to, template string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		var n int
		if err := pool.QueryRow(context.Background(),
			`SELECT count(*) FROM email_deliveries
			 WHERE org_id = $1 AND to_email = $2 AND template = $3 AND status = 'sent'`,
			orgID, to, template,
		).Scan(&n); err != nil {
			t.Fatalf("count deliveries: %v", err)
		}
		if n == 1 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for sent %s delivery to %s (found %d)", template, to, n)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestTicketEventPipelineEndToEnd exercises the production chain with real
// PostgreSQL rows and a real asynq server on in-process Redis:
//
//	ticket create -> SLA due dates -> ticket_events outbox row ->
//	outbox consumer mapping -> asynq email task -> email planning ->
//	delivery persistence -> SMTP sender
//
// It then replays the same task to prove the consumer is idempotent, and drives
// the breach and auto-close checkers to prove their events flow through the
// same pipeline.
func TestTicketEventPipelineEndToEnd(t *testing.T) {
	pool := dispatcherTestPool(t)
	fixture := seedPipelineFixture(t, pool)

	mr := miniredis.RunT(t)
	cfg := queue.Config{
		Redis:           queue.RedisOptions{Addr: mr.Addr()},
		IOConcurrency:   2,
		CPUConcurrency:  1,
		ShutdownTimeout: 5 * time.Second,
	}

	sender := &pipelineSender{}
	emailRepo := email.NewRepository(pool)
	emailSvc := email.NewService(emailRepo)
	ratingTokens := ratings.NewTokenManager(strings.Repeat("p", 32), time.Hour)
	emailSvc.SetRatingLinks(ratingTokens, "https://support.pipeline.test")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	emailHandler := email.NewTaskHandler(emailSvc, sender, logger)

	mux := worker.NewMux(worker.HandlerRegistration{
		Type:    queue.TaskEmailSend,
		Handler: emailHandler,
	})
	ioServer := queue.NewIOServer(cfg)
	if err := ioServer.Start(mux); err != nil {
		t.Fatalf("start io server: %v", err)
	}
	t.Cleanup(ioServer.Shutdown)

	client := queue.NewClient(cfg)
	t.Cleanup(func() { _ = client.Close() })

	ctx := context.Background()

	created := loadPipelineEvent(t, pool, fixture.ticketID, "ticket.created")
	if !created.CreatedAt.After(time.Now().Add(-time.Minute)) {
		t.Fatalf("ticket.created event is unexpectedly old: %s", created.CreatedAt)
	}
	job := enqueuePipelineEvent(t, pool, client, created)
	if job.ID != created.ID.String()+":email" {
		t.Fatalf("job id: %q, want %q", job.ID, created.ID.String()+":email")
	}
	if job.Type != queue.TaskEmailSend || job.Queue != queue.QueueCritical {
		t.Fatalf("job routing: type=%s queue=%s", job.Type, job.Queue)
	}

	waitForSentDelivery(t, pool, fixture.orgID, fixture.requesterEmail, email.TemplateTicketCreated)
	sent := sender.snapshot()
	if len(sent) != 1 || sent[0].to != fixture.requesterEmail || sent[0].template != email.TemplateTicketCreated {
		t.Fatalf("ticket.created sends: %+v", sent)
	}

	// Replay the task exactly as the at-least-once outbox would. The unique
	// (event_id, to_email, template) row and the sent status must absorb it.
	for i := 0; i < 2; i++ {
		if err := emailHandler.ProcessTask(ctx, asynq.NewTask(queue.TaskEmailSend, job.Payload)); err != nil {
			t.Fatalf("replay %d: %v", i, err)
		}
	}
	waitForSentDelivery(t, pool, fixture.orgID, fixture.requesterEmail, email.TemplateTicketCreated)
	if got := sender.snapshot(); len(got) != 1 {
		t.Fatalf("replayed task sent again: %+v", got)
	}
	var ticketCreatedDeliveries int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM email_deliveries
		 WHERE org_id = $1 AND event_id = $2 AND template = $3`,
		fixture.orgID, created.ID, email.TemplateTicketCreated,
	).Scan(&ticketCreatedDeliveries); err != nil {
		t.Fatalf("count deliveries: %v", err)
	}
	if ticketCreatedDeliveries != 1 {
		t.Fatalf("ticket.created deliveries: %d, want 1", ticketCreatedDeliveries)
	}

	// Force a first-response breach and drive the checker.
	if _, err := pool.Exec(ctx,
		`UPDATE tickets
		 SET first_response_due_at = now() - interval '1 minute',
		     resolution_due_at = now() + interval '1 hour',
		     updated_at = now()
		 WHERE id = $1`,
		fixture.ticketID,
	); err != nil {
		t.Fatalf("age SLA deadline: %v", err)
	}
	checker := tickets.NewBreachChecker(fixture.repo)
	if n, err := checker.CheckBreaches(ctx, 1000); err != nil || n < 1 {
		t.Fatalf("breach check: n=%d err=%v", n, err)
	}
	var firstResponseBreached *time.Time
	if err := pool.QueryRow(ctx,
		`SELECT first_response_breached_at FROM tickets WHERE id = $1`,
		fixture.ticketID,
	).Scan(&firstResponseBreached); err != nil {
		t.Fatalf("load breach marker: %v", err)
	}
	if firstResponseBreached == nil {
		t.Fatal("first response breach was not recorded")
	}

	breached := loadPipelineEvent(t, pool, fixture.ticketID, "sla.breached")
	breachJob := enqueuePipelineEvent(t, pool, client, breached)
	if breachJob.ID != breached.ID.String()+":email" {
		t.Fatalf("breach job id: %q", breachJob.ID)
	}
	waitForSentDelivery(t, pool, fixture.orgID, fixture.supportEmail, email.TemplateSLABreached)
	sent = sender.snapshot()
	if len(sent) != 2 {
		t.Fatalf("sends after breach: %+v", sent)
	}

	// A solved status change is the only status transition with an email
	// consumer. Insert the outbox row directly, then run it through the queue.
	var solvedEventID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO ticket_events (org_id, ticket_id, actor_type, event_type, data)
		 VALUES ($1, $2, 'system', 'ticket.status_changed', '{"old":"open","new":"solved"}')
		 RETURNING id`,
		fixture.orgID, fixture.ticketID,
	).Scan(&solvedEventID); err != nil {
		t.Fatalf("insert solved event: %v", err)
	}
	solved := loadPipelineEvent(t, pool, fixture.ticketID, "ticket.status_changed")
	if solved.ID != solvedEventID {
		t.Fatalf("solved event: %s, want %s", solved.ID, solvedEventID)
	}
	solvedJob := enqueuePipelineEvent(t, pool, client, solved)
	if solvedJob.ID != solvedEventID.String()+":email" {
		t.Fatalf("solved job id: %q", solvedJob.ID)
	}

	waitForSentDelivery(t, pool, fixture.orgID, fixture.requesterEmail, email.TemplateTicketSolved)
	sent = sender.snapshot()
	if len(sent) != 3 {
		t.Fatalf("sends after solved: %+v", sent)
	}
	solvedEmail := sent[2]
	if solvedEmail.template != email.TemplateTicketSolved ||
		solvedEmail.payload.RatingURL == "" {
		t.Fatalf("solved email: %+v", solvedEmail)
	}
	link, err := url.Parse(solvedEmail.payload.RatingURL)
	if err != nil {
		t.Fatalf("parse rating url: %v", err)
	}
	claims, err := ratingTokens.Verify(link.Query().Get("token"))
	if err != nil {
		t.Fatalf("verify rating token: %v", err)
	}
	if claims.TicketID != fixture.ticketID {
		t.Fatalf("rating token ticket: %s", claims.TicketID)
	}

	// A non-solved transition is filtered before enqueueing, so it creates no
	// job and no customer email.
	var pendingEventID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO ticket_events (org_id, ticket_id, actor_type, event_type, data)
		 VALUES ($1, $2, 'system', 'ticket.status_changed', '{"old":"open","new":"pending"}')
		 RETURNING id`,
		fixture.orgID, fixture.ticketID,
	).Scan(&pendingEventID); err != nil {
		t.Fatalf("insert pending event: %v", err)
	}
	pending := loadPipelineEvent(t, pool, fixture.ticketID, "ticket.status_changed")
	if pending.ID != pendingEventID {
		t.Fatalf("pending event: %s, want %s", pending.ID, pendingEventID)
	}
	if consumers := ConsumersFor(pending); len(consumers) != 1 || consumers[0].Name != ConsumerWebhook {
		t.Fatalf("pending status change consumers: %+v, want webhook only", consumers)
	}
	if got := sender.snapshot(); len(got) != 3 {
		t.Fatalf("pending status change sent an email: %+v", got)
	}

	// Move the ticket through solved -> closed and verify the auto-close event.
	if _, err := pool.Exec(ctx,
		`UPDATE tickets
		 SET status = 'solved', resolved_at = now() - interval '8 days', updated_at = now()
		 WHERE id = $1`,
		fixture.ticketID,
	); err != nil {
		t.Fatalf("age solved ticket: %v", err)
	}
	autoClose := tickets.NewAutoCloseChecker(fixture.repo, 7*24*time.Hour)
	if n, err := autoClose.CheckAutoClose(ctx, 1000); err != nil || n < 1 {
		t.Fatalf("auto close check: n=%d err=%v", n, err)
	}
	var status string
	if err := pool.QueryRow(ctx,
		`SELECT status FROM tickets WHERE id = $1`, fixture.ticketID,
	).Scan(&status); err != nil {
		t.Fatalf("load status: %v", err)
	}
	if status != "closed" {
		t.Fatalf("ticket status: %s, want closed", status)
	}
	loadPipelineEvent(t, pool, fixture.ticketID, "ticket.closed")
	if consumers := ConsumersFor(Event{EventType: "ticket.closed"}); len(consumers) != 1 || consumers[0].Name != ConsumerWebhook {
		t.Fatalf("ticket.closed consumers: %+v, want webhook only", consumers)
	}
	if got := sender.snapshot(); len(got) != 3 {
		t.Fatalf("auto-close sent email: %+v", got)
	}
}
