package webhooks

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"testing"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"

	"github.com/HenriNorman/gregport/internal/queue"
)

func dispatchTask(t *testing.T, event OutboxEvent) *asynq.Task {
	t.Helper()
	payload, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return asynq.NewTask(queue.TaskWebhookDispatch, payload)
}

func deliverTask(t *testing.T, deliveryID uuid.UUID) *asynq.Task {
	t.Helper()
	payload, err := json.Marshal(DeliveryTaskPayload{DeliveryID: deliveryID})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return asynq.NewTask(queue.TaskWebhookDeliver, payload)
}

func TestTaskHandlerDispatch(t *testing.T) {
	orgID := uuid.New()
	store := newFakeStore()
	sender := &fakeSender{response: Response{StatusCode: 200}}
	enqueuer := &fakeEnqueuer{}
	svc := NewService(store)
	svc.SetSender(sender)
	svc.SetEnqueuer(enqueuer)
	svc.SetResolver(fakeResolver{addrs: map[string][]netip.Addr{
		"public.example": {netip.MustParseAddr("93.184.216.34")},
	}})
	h := NewTaskHandler(svc, nil)

	sub, err := svc.Create(context.Background(), orgID, CreateSubscriptionRequest{
		URL:    "https://public.example/hooks",
		Events: []string{EventTicketCreated},
	})
	if err != nil {
		t.Fatal(err)
	}
	ticketID := uuid.New()
	store.ticket = &TicketSummary{ID: ticketID, Number: 1042, Subject: "Hi", Status: "open", Priority: "normal"}

	event := OutboxEvent{
		EventID:   uuid.New(),
		OrgID:     orgID,
		TicketID:  ticketID,
		EventType: EventTicketCreated,
		Data:      json.RawMessage(`{"number":1042}`),
	}

	if err := h.Dispatch(context.Background(), dispatchTask(t, event)); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if got := len(enqueuer.snapshot()); got != 1 {
		t.Fatalf("jobs=%d", got)
	}
	if enqueuer.snapshot()[0].Type != queue.TaskWebhookDeliver {
		t.Fatalf("job type=%s", enqueuer.snapshot()[0].Type)
	}

	job := enqueuer.snapshot()[0]
	var tp DeliveryTaskPayload
	if err := json.Unmarshal(job.Payload, &tp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	d, gotSub, err := store.LoadDeliveryForAttempt(context.Background(), tp.DeliveryID)
	if err != nil || d.SubscriptionID != sub.ID || gotSub.ID != sub.ID {
		t.Fatalf("delivery: %+v %+v err=%v", d, gotSub, err)
	}
}

func TestTaskHandlerDispatchDecodeFailureIsPermanent(t *testing.T) {
	h := NewTaskHandler(NewService(newFakeStore()), nil)
	bad := asynq.NewTask(queue.TaskWebhookDispatch, []byte(`{not json`))
	if err := h.Dispatch(context.Background(), bad); !errors.Is(err, asynq.SkipRetry) {
		t.Fatalf("err=%v, want SkipRetry", err)
	}
}

func TestTaskHandlerDispatchUnknownEventNoop(t *testing.T) {
	store := newFakeStore()
	enqueuer := &fakeEnqueuer{}
	svc := NewService(store)
	svc.SetEnqueuer(enqueuer)
	h := NewTaskHandler(svc, nil)

	event := OutboxEvent{
		EventID:   uuid.New(),
		OrgID:     uuid.New(),
		TicketID:  uuid.New(),
		EventType: EventPing,
		Data:      json.RawMessage(`{}`),
	}
	if err := h.Dispatch(context.Background(), dispatchTask(t, event)); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if len(enqueuer.snapshot()) != 0 {
		t.Fatal("unknown event enqueued a job")
	}
}

func TestTaskHandlerDeliver(t *testing.T) {
	store := newFakeStore()
	sender := &fakeSender{response: Response{StatusCode: 200}}
	svc := NewService(store)
	svc.SetSender(sender)
	h := NewTaskHandler(svc, nil)

	sub, err := store.Create(context.Background(), uuid.New(), Subscription{
		URL: "https://example.test/hooks", Events: []string{EventTicketCreated}, Secret: "s",
	})
	if err != nil {
		t.Fatal(err)
	}
	d, _, err := store.InsertDelivery(context.Background(), Delivery{
		OrgID:          sub.OrgID,
		SubscriptionID: sub.ID,
		EventType:      EventTicketCreated,
		Payload:        json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := h.Deliver(context.Background(), deliverTask(t, d.ID)); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if sender.count() != 1 {
		t.Fatalf("calls=%d", sender.count())
	}
	loaded, _, err := store.LoadDeliveryForAttempt(context.Background(), d.ID)
	if err != nil || loaded.Status != StatusSent {
		t.Fatalf("delivery: %+v err=%v", loaded, err)
	}
}

func TestTaskHandlerDeliverDecodeFailureIsPermanent(t *testing.T) {
	h := NewTaskHandler(NewService(newFakeStore()), nil)
	bad := asynq.NewTask(queue.TaskWebhookDeliver, []byte(`{not json`))
	if err := h.Deliver(context.Background(), bad); !errors.Is(err, asynq.SkipRetry) {
		t.Fatalf("err=%v, want SkipRetry", err)
	}
}

func TestTaskHandlerDeliverMissingDeliveryIDIsPermanent(t *testing.T) {
	h := NewTaskHandler(NewService(newFakeStore()), nil)
	task := asynq.NewTask(queue.TaskWebhookDeliver, []byte(`{"delivery_id":"00000000-0000-0000-0000-000000000000"}`))
	if err := h.Deliver(context.Background(), task); !errors.Is(err, asynq.SkipRetry) {
		t.Fatalf("err=%v, want SkipRetry", err)
	}
}

func TestTaskHandlerDeliverNoSenderIsPermanent(t *testing.T) {
	store := newFakeStore()
	sub, err := store.Create(context.Background(), uuid.New(), Subscription{
		URL: "https://example.test/hooks", Events: []string{EventTicketCreated}, Secret: "s",
	})
	if err != nil {
		t.Fatal(err)
	}
	d, _, err := store.InsertDelivery(context.Background(), Delivery{
		OrgID:          sub.OrgID,
		SubscriptionID: sub.ID,
		EventType:      EventTicketCreated,
		Payload:        json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	h := NewTaskHandler(NewService(store), nil) // no sender
	if err := h.Deliver(context.Background(), deliverTask(t, d.ID)); !errors.Is(err, asynq.SkipRetry) {
		t.Fatalf("err=%v, want SkipRetry", err)
	}
}

func TestTaskHandlerDeliverMissingDeliveryIsNoop(t *testing.T) {
	h := NewTaskHandler(NewService(newFakeStore()), nil)
	if err := h.Deliver(context.Background(), deliverTask(t, uuid.New())); err != nil {
		t.Fatalf("deliver missing: %v", err)
	}
}
