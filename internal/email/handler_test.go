package email

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
)

type fakeRepo struct {
	tickets        map[uuid.UUID]TicketContext
	comments       map[uuid.UUID]CommentContext
	deliveries     []Delivery
	markSentCalls  []uuid.UUID
	recordAttempts []struct {
		id        uuid.UUID
		status    string
		attempts  int
		nextRetry *time.Time
		lastError *string
	}
	insertErr error
}

func (f *fakeRepo) LoadTicketContext(ctx context.Context, orgID, ticketID uuid.UUID) (TicketContext, error) {
	if tc, ok := f.tickets[ticketID]; ok {
		return tc, nil
	}
	return TicketContext{}, ErrTicketNotFound
}

func (f *fakeRepo) LoadComment(ctx context.Context, orgID, commentID uuid.UUID) (CommentContext, error) {
	if c, ok := f.comments[commentID]; ok {
		return c, nil
	}
	return CommentContext{}, ErrCommentNotFound
}

func (f *fakeRepo) InsertDelivery(ctx context.Context, orgID, eventID uuid.UUID, planned PlannedDelivery) (Delivery, error) {
	if f.insertErr != nil {
		return Delivery{}, f.insertErr
	}
	// Conflict detection on (event_id, to_email, template) mirrors the
	// partial unique index: replaying the same event returns the existing row.
	for _, d := range f.deliveries {
		if d.EventID == eventID && d.ToEmail == planned.ToEmail && d.Template == planned.Template {
			return d, nil
		}
	}
	d := Delivery{
		ID:        uuid.New(),
		OrgID:     orgID,
		EventID:   eventID,
		ToEmail:   planned.ToEmail,
		Template:  planned.Template,
		Payload:   mustMarshal(planned.Payload),
		Status:    StatusPending,
		CreatedAt: time.Now(),
	}
	f.deliveries = append(f.deliveries, d)
	return d, nil
}

func (f *fakeRepo) MarkSent(ctx context.Context, id uuid.UUID) error {
	f.markSentCalls = append(f.markSentCalls, id)
	return nil
}

func (f *fakeRepo) RecordAttempt(ctx context.Context, id uuid.UUID, status string, attempts int, nextRetryAt *time.Time, lastError *string) error {
	f.recordAttempts = append(f.recordAttempts, struct {
		id        uuid.UUID
		status    string
		attempts  int
		nextRetry *time.Time
		lastError *string
	}{id, status, attempts, nextRetryAt, lastError})
	return nil
}

type fakeSender struct {
	sent []struct {
		to, template string
		payload      DeliveryPayload
	}
	fail    bool
	failErr error
}

func (f *fakeSender) Send(_ context.Context, to, template string, payload DeliveryPayload) error {
	f.sent = append(f.sent, struct {
		to, template string
		payload      DeliveryPayload
	}{to, template, payload})
	if f.fail {
		if f.failErr != nil {
			return f.failErr
		}
		return errors.New("smtp down")
	}
	return nil
}

func mustMarshal(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func ticketContext() TicketContext {
	return TicketContext{
		OrgID:          uuid.New(),
		TicketID:       uuid.New(),
		Number:         1042,
		Subject:        "Cannot log in",
		RequesterEmail: "customer@acme.test",
		RequesterName:  "Ada Customer",
		AssigneeEmail:  "agent@acme.test",
		AssigneeName:   "Grace Agent",
		OrgName:        "Acme",
		SupportEmail:   "support@acme.test",
	}
}

func payload(eventType string) TaskPayload {
	return TaskPayload{
		EventID:   uuid.New(),
		OrgID:     ticketContext().OrgID,
		TicketID:  ticketContext().TicketID,
		EventType: eventType,
		Data:      json.RawMessage(`{"number":1042}`),
	}
}

func newHandler(t *testing.T, repo *fakeRepo, sender *fakeSender) *TaskHandler {
	t.Helper()
	return NewTaskHandler(NewService(repo), sender, nil)
}

func TestHandlerSkipsUnknownEvent(t *testing.T) {
	repo := &fakeRepo{tickets: map[uuid.UUID]TicketContext{}}
	sender := &fakeSender{}
	h := newHandler(t, repo, sender)

	tk := ticketContext()
	repo.tickets[tk.TicketID] = tk

	p := payload("ticket.updated")
	err := h.ProcessTask(context.Background(), asynqTask(p))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sender.sent) != 0 {
		t.Fatalf("sender was called: %+v", sender.sent)
	}
	if len(repo.deliveries) != 0 {
		t.Fatalf("deliveries were created: %+v", repo.deliveries)
	}
}

func TestHandlerSkipsMissingTicket(t *testing.T) {
	repo := &fakeRepo{tickets: map[uuid.UUID]TicketContext{}}
	sender := &fakeSender{}
	h := newHandler(t, repo, sender)

	err := h.ProcessTask(context.Background(), asynqTask(payload("ticket.created")))
	if err != nil {
		t.Fatalf("expected nil (terminal), got %v", err)
	}
	if len(sender.sent) != 0 {
		t.Fatalf("sender was called: %+v", sender.sent)
	}
}

func TestHandlerCreatesAndSendsDelivery(t *testing.T) {
	repo := &fakeRepo{tickets: map[uuid.UUID]TicketContext{}}
	sender := &fakeSender{}
	h := newHandler(t, repo, sender)

	tk := ticketContext()
	repo.tickets[tk.TicketID] = tk

	p := payload("ticket.created")
	p.TicketID = tk.TicketID
	p.OrgID = tk.OrgID

	err := h.ProcessTask(context.Background(), asynqTask(p))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(repo.deliveries) != 1 {
		t.Fatalf("expected 1 delivery, got %d", len(repo.deliveries))
	}
	d := repo.deliveries[0]
	if d.ToEmail != tk.RequesterEmail || d.Template != TemplateTicketCreated {
		t.Fatalf("delivery: %+v", d)
	}
	if d.Status != StatusPending {
		t.Fatalf("expected pending, got %s", d.Status)
	}

	// The delivery is still pending after this task because sendOne calls
	// MarkSent, which only appends to markSentCalls; check it was marked.
	if len(repo.markSentCalls) != 1 {
		t.Fatalf("expected MarkSent called once, got %d", len(repo.markSentCalls))
	}
	if len(sender.sent) != 1 || sender.sent[0].to != tk.RequesterEmail {
		t.Fatalf("sender: %+v", sender.sent)
	}
}

func TestHandlerSkipsAlreadySentDelivery(t *testing.T) {
	repo := &fakeRepo{tickets: map[uuid.UUID]TicketContext{}}
	sender := &fakeSender{}
	h := newHandler(t, repo, sender)

	tk := ticketContext()
	repo.tickets[tk.TicketID] = tk

	// Pre-create a sent delivery for this event/template/recipient.
	eventID := uuid.New()
	sentID := uuid.New()
	repo.deliveries = append(repo.deliveries, Delivery{
		ID:       sentID,
		OrgID:    tk.OrgID,
		EventID:  eventID,
		ToEmail:  tk.RequesterEmail,
		Template: TemplateTicketCreated,
		Payload:  mustMarshal(DeliveryPayload{}),
		Status:   StatusSent,
	})

	p := payload("ticket.created")
	p.EventID = eventID
	p.TicketID = tk.TicketID

	err := h.ProcessTask(context.Background(), asynqTask(p))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(sender.sent) != 0 {
		t.Fatalf("sender should not be called for sent delivery: %+v", sender.sent)
	}
	if len(repo.markSentCalls) != 0 {
		t.Fatalf("MarkSent should not be called for sent delivery")
	}
}

func TestHandlerSkipsDeadDelivery(t *testing.T) {
	repo := &fakeRepo{tickets: map[uuid.UUID]TicketContext{}}
	sender := &fakeSender{}
	h := newHandler(t, repo, sender)

	tk := ticketContext()
	repo.tickets[tk.TicketID] = tk

	eventID := uuid.New()
	deadID := uuid.New()
	repo.deliveries = append(repo.deliveries, Delivery{
		ID:       deadID,
		OrgID:    tk.OrgID,
		EventID:  eventID,
		ToEmail:  tk.RequesterEmail,
		Template: TemplateTicketCreated,
		Payload:  mustMarshal(DeliveryPayload{}),
		Status:   StatusDead,
	})

	p := payload("ticket.created")
	p.EventID = eventID
	p.TicketID = tk.TicketID

	err := h.ProcessTask(context.Background(), asynqTask(p))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sender.sent) != 0 {
		t.Fatalf("sender should not be called for dead delivery: %+v", sender.sent)
	}
}

func TestHandlerSenderFailureRecordsAttemptAndContinues(t *testing.T) {
	repo := &fakeRepo{tickets: map[uuid.UUID]TicketContext{}}
	sender := &fakeSender{fail: true}
	h := newHandler(t, repo, sender)

	tk := ticketContext()
	repo.tickets[tk.TicketID] = tk

	p := payload("ticket.created")
	p.TicketID = tk.TicketID
	p.OrgID = tk.OrgID

	// Two deliveries for the same event: requester + support (sla breach to assignee then support).
	// Use ticket.created which only has one recipient, so just one delivery.
	err := h.ProcessTask(context.Background(), asynqTask(p))
	if err != nil {
		t.Fatalf("expected nil (failure is logged, not returned), got %v", err)
	}

	if len(sender.sent) != 1 {
		t.Fatalf("sender called once, got %d", len(sender.sent))
	}
	if len(repo.recordAttempts) != 1 {
		t.Fatalf("expected 1 recordAttempt, got %d", len(repo.recordAttempts))
	}
	ra := repo.recordAttempts[0]
	if ra.status != StatusFailed || ra.attempts != 1 {
		t.Fatalf("recordAttempt: %+v", ra)
	}
	if ra.nextRetry == nil {
		t.Fatal("expected nextRetry to be set")
	}
	if ra.lastError == nil || *ra.lastError == "" {
		t.Fatal("expected lastError to be set")
	}
}

func TestHandlerSenderFailureMarksDeadAfterMaxAttempts(t *testing.T) {
	repo := &fakeRepo{tickets: map[uuid.UUID]TicketContext{}}
	sender := &fakeSender{fail: true}
	h := newHandler(t, repo, sender)

	tk := ticketContext()
	repo.tickets[tk.TicketID] = tk

	// Pre-create a pending delivery at maxAttempts-1 so the next failure goes
	// dead. InsertDelivery's conflict detection returns this same row.
	eventID := uuid.New()
	repo.deliveries = append(repo.deliveries, Delivery{
		ID:       uuid.New(),
		OrgID:    tk.OrgID,
		EventID:  eventID,
		ToEmail:  tk.RequesterEmail,
		Template: TemplateTicketCreated,
		Payload:  mustMarshal(DeliveryPayload{}),
		Status:   StatusPending,
		Attempts: maxDeliveryAttempts - 1,
	})

	p := payload("ticket.created")
	p.EventID = eventID
	p.TicketID = tk.TicketID
	p.OrgID = tk.OrgID

	err := h.ProcessTask(context.Background(), asynqTask(p))
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}

	dead := false
	for _, ra := range repo.recordAttempts {
		if ra.status == StatusDead {
			dead = true
		}
	}
	if !dead {
		t.Fatalf("expected a dead recordAttempt, got %+v", repo.recordAttempts)
	}
}

func TestHandlerMalformedPayloadIsPermanent(t *testing.T) {
	repo := &fakeRepo{tickets: map[uuid.UUID]TicketContext{}}
	sender := &fakeSender{}
	h := newHandler(t, repo, sender)

	task := asynq.NewTask("email:send", []byte(`not-json`))
	err := h.ProcessTask(context.Background(), task)
	if err == nil {
		t.Fatal("expected an error for malformed payload")
	}
}

func TestHandlerValidateFailureIsPermanent(t *testing.T) {
	repo := &fakeRepo{tickets: map[uuid.UUID]TicketContext{}}
	sender := &fakeSender{}
	h := newHandler(t, repo, sender)

	task := asynq.NewTask("email:send", []byte(`{"event_type":"ticket.created"}`))
	err := h.ProcessTask(context.Background(), task)
	if err == nil {
		t.Fatal("expected a validation error")
	}
}

func TestHandlerCommentAddedRoutesToAssignee(t *testing.T) {
	repo := &fakeRepo{
		tickets:  map[uuid.UUID]TicketContext{},
		comments: map[uuid.UUID]CommentContext{},
	}
	sender := &fakeSender{}
	h := newHandler(t, repo, sender)

	tk := ticketContext()
	repo.tickets[tk.TicketID] = tk

	commentID := uuid.New()
	repo.comments[commentID] = CommentContext{
		AuthorType: "customer",
		Body:       "Still broken",
		IsInternal: false,
	}

	p := payload("ticket.created") // reuse the same ticket/org ids
	p.EventType = "comment.added"
	p.TicketID = tk.TicketID
	p.Data = mustMarshal(map[string]any{"comment_id": commentID})

	err := h.ProcessTask(context.Background(), asynqTask(p))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sender.sent) != 1 || sender.sent[0].to != tk.AssigneeEmail {
		t.Fatalf("expected email to assignee, got %+v", sender.sent)
	}
}

func asynqTask(p TaskPayload) *asynq.Task {
	b, err := json.Marshal(p)
	if err != nil {
		panic(err)
	}
	return asynq.NewTask("email:send", b)
}
