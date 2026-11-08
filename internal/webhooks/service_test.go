package webhooks

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/HenriNorman/gregport/internal/queue"
)

func serviceFixture(t *testing.T) (*Service, *fakeStore, *fakeSender, *fakeEnqueuer, uuid.UUID) {
	t.Helper()
	store := newFakeStore()
	sender := &fakeSender{response: Response{StatusCode: 200, Duration: 5 * time.Millisecond}}
	enqueuer := &fakeEnqueuer{}
	svc := NewService(store)
	svc.SetSender(sender)
	svc.SetEnqueuer(enqueuer)
	svc.SetResolver(fakeResolver{addrs: map[string][]netip.Addr{
		"public.example":  {netip.MustParseAddr("93.184.216.34")},
		"private.example": {netip.MustParseAddr("10.0.0.7")},
	}})
	return svc, store, sender, enqueuer, uuid.New()
}

func createSub(t *testing.T, svc *Service, store *fakeStore, orgID uuid.UUID, events ...string) Subscription {
	t.Helper()
	sub, err := svc.Create(context.Background(), orgID, CreateSubscriptionRequest{
		URL:    "https://public.example/hooks",
		Events: events,
	})
	if err != nil {
		t.Fatalf("create subscription: %v", err)
	}
	return sub
}

func TestServiceCreateValidatesAndNormalizes(t *testing.T) {
	svc, _, _, _, orgID := serviceFixture(t)

	if _, err := svc.Create(context.Background(), orgID, CreateSubscriptionRequest{
		URL: "https://private.example/hooks", Events: []string{EventTicketCreated},
	}); !errors.Is(err, ErrInvalidURL) {
		t.Fatalf("private url err=%v", err)
	}
	for _, events := range [][]string{nil, {}, {EventTicketCreated, "unknown.event"}} {
		if _, err := svc.Create(context.Background(), orgID, CreateSubscriptionRequest{
			URL: "https://public.example/hooks", Events: events,
		}); !errors.Is(err, ErrInvalidEvents) {
			t.Fatalf("events %v err=%v", events, err)
		}
	}

	sub, err := svc.Create(context.Background(), orgID, CreateSubscriptionRequest{
		URL:    " https://public.example/hooks ",
		Events: []string{EventTicketCreated, EventTicketCreated, " " + EventCommentAdded},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(sub.Events) != 2 || sub.Events[0] != EventTicketCreated || sub.Events[1] != EventCommentAdded {
		t.Fatalf("events: %+v", sub.Events)
	}
	if sub.URL != "https://public.example/hooks" {
		t.Fatalf("url: %q", sub.URL)
	}
	if len(sub.Secret) < 32 || sub.Secret[:6] != "whsec_" {
		t.Fatalf("secret: %q", sub.Secret)
	}
	if !sub.IsActive {
		t.Fatal("new subscription should be active")
	}

	listed, err := svc.List(context.Background(), orgID)
	if err != nil || len(listed) != 1 {
		t.Fatalf("list=%+v err=%v", listed, err)
	}
	if dto := toSubscriptionDTO(listed[0], false); dto.Secret != "" {
		t.Fatalf("list leaked secret: %q", dto.Secret)
	}
}

func TestServiceUpdateReEnablesAndResetsFailures(t *testing.T) {
	svc, store, _, _, orgID := serviceFixture(t)
	sub := createSub(t, svc, store, orgID, EventTicketCreated)

	disabledAt := time.Now().UTC()
	sub.IsActive = false
	sub.ConsecutiveFailures = 5
	sub.DisabledAt = &disabledAt
	store.subs[sub.ID] = sub

	active := true
	updated, err := svc.Update(context.Background(), orgID, sub.ID, UpdateSubscriptionRequest{IsActive: &active})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if !updated.IsActive || updated.ConsecutiveFailures != 0 || updated.DisabledAt != nil {
		t.Fatalf("updated: %+v", updated)
	}

	events := []string{EventCommentAdded}
	url := "https://public.example/other"
	updated, err = svc.Update(context.Background(), orgID, sub.ID, UpdateSubscriptionRequest{URL: &url, Events: &events})
	if err != nil {
		t.Fatalf("update fields: %v", err)
	}
	if updated.URL != url || len(updated.Events) != 1 || updated.Events[0] != EventCommentAdded {
		t.Fatalf("updated fields: %+v", updated)
	}
}

func TestServiceTenantIsolation(t *testing.T) {
	svc, store, _, _, orgID := serviceFixture(t)
	sub := createSub(t, svc, store, orgID, EventTicketCreated)
	otherOrg := uuid.New()

	if _, err := svc.Get(context.Background(), otherOrg, sub.ID); !errors.Is(err, ErrWebhookNotFound) {
		t.Fatalf("foreign get err=%v", err)
	}
	if _, err := svc.Update(context.Background(), otherOrg, sub.ID, UpdateSubscriptionRequest{}); !errors.Is(err, ErrWebhookNotFound) {
		t.Fatalf("foreign update err=%v", err)
	}
	if err := svc.Delete(context.Background(), otherOrg, sub.ID); !errors.Is(err, ErrWebhookNotFound) {
		t.Fatalf("foreign delete err=%v", err)
	}
}

func TestServiceDispatchPlansIdempotently(t *testing.T) {
	svc, store, _, enqueuer, orgID := serviceFixture(t)
	matching := createSub(t, svc, store, orgID, EventTicketCreated)
	createSub(t, svc, store, orgID, EventCommentAdded)

	ticketID := uuid.New()
	store.ticket = &TicketSummary{ID: ticketID, Number: 1042, Subject: "Cannot log in", Status: "open", Priority: "normal"}
	event := OutboxEvent{
		EventID:   uuid.New(),
		OrgID:     orgID,
		TicketID:  ticketID,
		EventType: EventTicketCreated,
		Data:      json.RawMessage(`{"number":1042,"subject":"Cannot log in"}`),
	}

	if err := svc.Dispatch(context.Background(), event); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	jobs := enqueuer.snapshot()
	if len(jobs) != 1 {
		t.Fatalf("jobs: %+v", jobs)
	}
	if jobs[0].Type != queue.TaskWebhookDeliver || jobs[0].Queue != queue.QueueDefault {
		t.Fatalf("job: %+v", jobs[0])
	}
	var task DeliveryTaskPayload
	if err := json.Unmarshal(jobs[0].Payload, &task); err != nil {
		t.Fatalf("decode task: %v", err)
	}
	delivery, sub, err := store.LoadDeliveryForAttempt(context.Background(), task.DeliveryID)
	if err != nil {
		t.Fatalf("load delivery: %v", err)
	}
	if delivery.SubscriptionID != matching.ID || sub.ID != matching.ID {
		t.Fatalf("delivery subscription: %s / %s", delivery.SubscriptionID, sub.ID)
	}
	var envelope Envelope
	if err := json.Unmarshal(delivery.Payload, &envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if envelope.ID != event.EventID || envelope.Type != EventTicketCreated {
		t.Fatalf("envelope: %+v", envelope)
	}
	if envelope.Data.Ticket == nil || envelope.Data.Ticket.Number != 1042 {
		t.Fatalf("ticket snapshot: %+v", envelope.Data.Ticket)
	}
	if string(envelope.Data.Event) != string(event.Data) {
		t.Fatalf("event data: %s", envelope.Data.Event)
	}

	// Replaying the outbox event reuses the delivery row and the task ID.
	if err := svc.Dispatch(context.Background(), event); err != nil {
		t.Fatalf("replay dispatch: %v", err)
	}
	if got := len(enqueuer.snapshot()); got != 1 {
		t.Fatalf("replay enqueued %d jobs", got)
	}
	if got := len(store.deliveries); got != 1 {
		t.Fatalf("replay created %d deliveries", got)
	}
}

func TestServiceDispatchUnknownEventIsNoop(t *testing.T) {
	svc, _, _, enqueuer, orgID := serviceFixture(t)
	err := svc.Dispatch(context.Background(), OutboxEvent{
		EventID: uuid.New(), OrgID: orgID, TicketID: uuid.New(), EventType: EventPing,
	})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if len(enqueuer.snapshot()) != 0 {
		t.Fatal("unknown event was enqueued")
	}
}

func TestServiceDeliverRetriesAndDies(t *testing.T) {
	svc, store, sender, _, orgID := serviceFixture(t)
	svc.SetFailureThreshold(1000)
	sub := createSub(t, svc, store, orgID, EventTicketCreated)
	sender.response = Response{StatusCode: 500, Body: []byte("boom"), Duration: 10 * time.Millisecond}

	delivery, _, err := store.InsertDelivery(context.Background(), Delivery{
		OrgID: orgID, SubscriptionID: sub.ID, EventType: EventTicketCreated, Payload: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("insert delivery: %v", err)
	}

	for attempt := 1; attempt <= queue.WebhookMaxAttempts(); attempt++ {
		err := svc.Deliver(context.Background(), delivery.ID)
		loaded, _, loadErr := store.LoadDeliveryForAttempt(context.Background(), delivery.ID)
		if loadErr != nil {
			t.Fatalf("load attempt %d: %v", attempt, loadErr)
		}
		if attempt < queue.WebhookMaxAttempts() {
			if err == nil {
				t.Fatalf("attempt %d returned success", attempt)
			}
			if loaded.Status != StatusFailed || loaded.Attempts != attempt {
				t.Fatalf("attempt %d delivery: %+v", attempt, loaded)
			}
			wantDelay := queue.WebhookRetryDelay(attempt)
			if loaded.NextRetryAt == nil || time.Until(*loaded.NextRetryAt) < wantDelay-time.Second {
				t.Fatalf("attempt %d next retry: %v want ~%s", attempt, loaded.NextRetryAt, wantDelay)
			}
		} else {
			if err != nil {
				t.Fatalf("terminal attempt returned error: %v", err)
			}
			if loaded.Status != StatusDead || loaded.NextRetryAt != nil {
				t.Fatalf("terminal delivery: %+v", loaded)
			}
		}
		if loaded.ResponseStatus == nil || *loaded.ResponseStatus != 500 {
			t.Fatalf("attempt %d response status: %v", attempt, loaded.ResponseStatus)
		}
		if loaded.ResponseBody == nil || *loaded.ResponseBody != "boom" {
			t.Fatalf("attempt %d response body: %v", attempt, loaded.ResponseBody)
		}
		if loaded.LastError == nil || *loaded.LastError != "HTTP 500" {
			t.Fatalf("attempt %d last error: %v", attempt, loaded.LastError)
		}
	}
}

func TestServiceDeliverDisablesSubscription(t *testing.T) {
	svc, store, sender, _, orgID := serviceFixture(t)
	svc.SetFailureThreshold(2)
	sub := createSub(t, svc, store, orgID, EventTicketCreated)
	sender.response = Response{StatusCode: 503, Duration: time.Millisecond}

	delivery, _, err := store.InsertDelivery(context.Background(), Delivery{
		OrgID: orgID, SubscriptionID: sub.ID, EventType: EventTicketCreated, Payload: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := svc.Deliver(context.Background(), delivery.ID); err == nil {
		t.Fatal("first failure returned success")
	}
	if err := svc.Deliver(context.Background(), delivery.ID); err == nil {
		t.Fatal("second failure returned success")
	}
	disabled, err := store.Get(context.Background(), orgID, sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	if disabled.IsActive || disabled.ConsecutiveFailures != 2 || disabled.DisabledAt == nil {
		t.Fatalf("subscription not disabled: %+v", disabled)
	}
	if sender.count() != 2 {
		t.Fatalf("sender calls: %d", sender.count())
	}

	// A planned delivery for a now-disabled subscription is closed without a
	// third HTTP attempt.
	if err := svc.Deliver(context.Background(), delivery.ID); err != nil {
		t.Fatalf("disabled delivery: %v", err)
	}
	loaded, _, err := store.LoadDeliveryForAttempt(context.Background(), delivery.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != StatusDead || loaded.LastError == nil {
		t.Fatalf("delivery: %+v", loaded)
	}
	if sender.count() != 2 {
		t.Fatalf("disabled subscription was contacted: %d calls", sender.count())
	}
}

func TestServiceDeliverSuccessResetsFailures(t *testing.T) {
	svc, store, sender, _, orgID := serviceFixture(t)
	sub := createSub(t, svc, store, orgID, EventTicketCreated)
	sub.ConsecutiveFailures = 3
	store.subs[sub.ID] = sub

	delivery, _, err := store.InsertDelivery(context.Background(), Delivery{
		OrgID: orgID, SubscriptionID: sub.ID, EventType: EventTicketCreated, Payload: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Deliver(context.Background(), delivery.ID); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	loaded, _, err := store.LoadDeliveryForAttempt(context.Background(), delivery.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != StatusSent || loaded.Attempts != 1 || loaded.DeliveredAt == nil {
		t.Fatalf("delivery: %+v", loaded)
	}
	updated, _ := store.Get(context.Background(), orgID, sub.ID)
	if updated.ConsecutiveFailures != 0 || updated.LastFailureAt != nil {
		t.Fatalf("failures not reset: %+v", updated)
	}

	// A replayed task is a no-op, not a second request.
	before := sender.count()
	if err := svc.Deliver(context.Background(), delivery.ID); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if sender.count() != before {
		t.Fatal("sent delivery was attempted again")
	}
}

func TestServiceTestAndRedeliver(t *testing.T) {
	svc, store, sender, _, orgID := serviceFixture(t)
	sub := createSub(t, svc, store, orgID, EventTicketCreated)

	ping, err := svc.Test(context.Background(), orgID, sub.ID)
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if ping.Status != StatusSent || ping.EventType != EventPing || ping.EventID != nil {
		t.Fatalf("ping: %+v", ping)
	}
	if sender.count() != 1 {
		t.Fatalf("sender calls: %d", sender.count())
	}

	delivery, _, err := store.InsertDelivery(context.Background(), Delivery{
		OrgID: orgID, SubscriptionID: sub.ID, EventType: EventTicketCreated, Payload: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	delivery.Status = StatusDead
	delivery.Attempts = 7
	delivery.LastError = strPtr("old")
	store.deliveries[delivery.ID] = delivery

	redelivered, err := svc.Redeliver(context.Background(), orgID, sub.ID, delivery.ID)
	if err != nil {
		t.Fatalf("redeliver: %v", err)
	}
	if redelivered.Status != StatusSent || redelivered.Attempts != 1 || redelivered.LastError != nil {
		t.Fatalf("redelivered: %+v", redelivered)
	}

	// Manual attempts do not count toward the circuit breaker.
	updated, _ := store.Get(context.Background(), orgID, sub.ID)
	if updated.ConsecutiveFailures != 0 {
		t.Fatalf("manual attempt changed failures: %+v", updated)
	}

	if _, err := svc.Redeliver(context.Background(), orgID, sub.ID, uuid.New()); !errors.Is(err, ErrDeliveryNotFound) {
		t.Fatalf("missing delivery err=%v", err)
	}
}

func TestServiceTestRequiresActiveSubscription(t *testing.T) {
	svc, store, _, _, orgID := serviceFixture(t)
	sub := createSub(t, svc, store, orgID, EventTicketCreated)
	sub.IsActive = false
	store.subs[sub.ID] = sub

	if _, err := svc.Test(context.Background(), orgID, sub.ID); !errors.Is(err, ErrInactive) {
		t.Fatalf("err=%v, want ErrInactive", err)
	}
}

func TestServiceWithoutSender(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store)
	orgID := uuid.New()
	sub, err := store.Create(context.Background(), orgID, Subscription{
		URL: "https://public.example/hooks", Events: []string{EventTicketCreated}, Secret: "s",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Test(context.Background(), orgID, sub.ID); !errors.Is(err, ErrNoSender) {
		t.Fatalf("err=%v, want ErrNoSender", err)
	}
}

func strPtr(s string) *string { return &s }
