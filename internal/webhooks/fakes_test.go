package webhooks

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/HenriNorman/gregport/internal/queue"
)

type fakeStore struct {
	mu         sync.Mutex
	subs       map[uuid.UUID]Subscription
	deliveries map[uuid.UUID]Delivery
	ticket     *TicketSummary
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		subs:       make(map[uuid.UUID]Subscription),
		deliveries: make(map[uuid.UUID]Delivery),
	}
}

func (f *fakeStore) List(_ context.Context, orgID uuid.UUID) ([]Subscription, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Subscription, 0)
	for _, sub := range f.subs {
		if sub.OrgID == orgID {
			out = append(out, sub)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (f *fakeStore) Get(_ context.Context, orgID, id uuid.UUID) (Subscription, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sub, ok := f.subs[id]
	if !ok || sub.OrgID != orgID {
		return Subscription{}, ErrWebhookNotFound
	}
	return sub, nil
}

func (f *fakeStore) Create(_ context.Context, orgID uuid.UUID, sub Subscription) (Subscription, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now().UTC()
	sub.ID = uuid.New()
	sub.OrgID = orgID
	sub.IsActive = true
	sub.CreatedAt = now
	sub.UpdatedAt = now
	f.subs[sub.ID] = sub
	return sub, nil
}

func (f *fakeStore) Update(_ context.Context, orgID uuid.UUID, sub Subscription, resetFailures bool) (Subscription, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	existing, ok := f.subs[sub.ID]
	if !ok || existing.OrgID != orgID {
		return Subscription{}, ErrWebhookNotFound
	}
	if resetFailures {
		sub.ConsecutiveFailures = 0
		sub.LastFailureAt = nil
		sub.DisabledAt = nil
	} else {
		sub.ConsecutiveFailures = existing.ConsecutiveFailures
		sub.LastFailureAt = existing.LastFailureAt
		sub.DisabledAt = existing.DisabledAt
	}
	sub.UpdatedAt = time.Now().UTC()
	f.subs[sub.ID] = sub
	return sub, nil
}

func (f *fakeStore) Delete(_ context.Context, orgID, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	sub, ok := f.subs[id]
	if !ok || sub.OrgID != orgID {
		return ErrWebhookNotFound
	}
	delete(f.subs, id)
	for deliveryID, d := range f.deliveries {
		if d.SubscriptionID == id {
			delete(f.deliveries, deliveryID)
		}
	}
	return nil
}

func (f *fakeStore) ListActiveForEvent(_ context.Context, orgID uuid.UUID, eventType string) ([]Subscription, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Subscription, 0)
	for _, sub := range f.subs {
		if sub.OrgID != orgID || !sub.IsActive {
			continue
		}
		for _, event := range sub.Events {
			if event == eventType {
				out = append(out, sub)
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (f *fakeStore) InsertDelivery(_ context.Context, d Delivery) (Delivery, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if d.EventID != nil {
		for _, existing := range f.deliveries {
			if existing.SubscriptionID == d.SubscriptionID && existing.EventID != nil && *existing.EventID == *d.EventID {
				return existing, false, nil
			}
		}
	}
	d.ID = uuid.New()
	d.Status = StatusPending
	d.Attempts = 0
	d.CreatedAt = time.Now().UTC()
	f.deliveries[d.ID] = d
	return d, true, nil
}

func (f *fakeStore) LoadDeliveryForAttempt(_ context.Context, deliveryID uuid.UUID) (Delivery, Subscription, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.deliveries[deliveryID]
	if !ok {
		return Delivery{}, Subscription{}, ErrDeliveryGone
	}
	sub, ok := f.subs[d.SubscriptionID]
	if !ok {
		return Delivery{}, Subscription{}, ErrDeliveryGone
	}
	return d, sub, nil
}

func (f *fakeStore) GetDeliveryScoped(_ context.Context, orgID, subscriptionID, deliveryID uuid.UUID) (Delivery, Subscription, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.deliveries[deliveryID]
	if !ok || d.OrgID != orgID || d.SubscriptionID != subscriptionID {
		return Delivery{}, Subscription{}, ErrDeliveryGone
	}
	sub, ok := f.subs[subscriptionID]
	if !ok {
		return Delivery{}, Subscription{}, ErrDeliveryGone
	}
	return d, sub, nil
}

func (f *fakeStore) ListDeliveries(_ context.Context, orgID, subscriptionID uuid.UUID, limit int, _ string) ([]Delivery, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Delivery, 0)
	for _, d := range f.deliveries {
		if d.OrgID == orgID && d.SubscriptionID == subscriptionID {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, "", nil
}

func (f *fakeStore) RecordDeliverySuccess(_ context.Context, d Delivery) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now().UTC()
	d.Status = StatusSent
	d.DeliveredAt = &now
	d.NextRetryAt = nil
	d.LastError = nil
	f.deliveries[d.ID] = d
	if sub, ok := f.subs[d.SubscriptionID]; ok {
		sub.ConsecutiveFailures = 0
		sub.LastFailureAt = nil
		f.subs[sub.ID] = sub
	}
	return nil
}

func (f *fakeStore) RecordDeliveryFailure(_ context.Context, d Delivery, threshold int, count bool) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deliveries[d.ID] = d
	if !count {
		return false, nil
	}
	sub, ok := f.subs[d.SubscriptionID]
	if !ok || !sub.IsActive {
		return false, nil
	}
	sub.ConsecutiveFailures++
	now := time.Now().UTC()
	sub.LastFailureAt = &now
	if threshold <= 0 {
		threshold = DefaultFailureThreshold
	}
	disabled := false
	if sub.ConsecutiveFailures >= threshold {
		sub.IsActive = false
		sub.DisabledAt = &now
		disabled = true
	}
	f.subs[sub.ID] = sub
	return disabled, nil
}

func (f *fakeStore) ResetDeliveryForRedelivery(_ context.Context, orgID, subscriptionID, deliveryID uuid.UUID) (Delivery, Subscription, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.deliveries[deliveryID]
	if !ok || d.OrgID != orgID || d.SubscriptionID != subscriptionID {
		return Delivery{}, Subscription{}, ErrDeliveryGone
	}
	d.Status = StatusPending
	d.Attempts = 0
	d.ResponseStatus = nil
	d.ResponseBody = nil
	d.LastError = nil
	d.NextRetryAt = nil
	d.DeliveredAt = nil
	d.DurationMS = nil
	f.deliveries[d.ID] = d
	sub, ok := f.subs[subscriptionID]
	if !ok {
		return Delivery{}, Subscription{}, ErrDeliveryGone
	}
	return d, sub, nil
}

func (f *fakeStore) LoadTicketSummary(_ context.Context, _, _ uuid.UUID) (TicketSummary, error) {
	if f.ticket == nil {
		return TicketSummary{}, ErrNotFound
	}
	return *f.ticket, nil
}

type fakeSender struct {
	mu       sync.Mutex
	response Response
	calls    []Delivery
}

func (f *fakeSender) Send(_ context.Context, _ Subscription, d Delivery) Response {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, d)
	return f.response
}

func (f *fakeSender) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

type fakeEnqueuer struct {
	mu   sync.Mutex
	jobs []jobRecord
	seen map[string]struct{}
	err  error
}

type jobRecord struct {
	ID      string
	Type    string
	Queue   string
	Payload []byte
}

func (f *fakeEnqueuer) EnqueueJob(_ context.Context, job queue.Job) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	if f.seen == nil {
		f.seen = make(map[string]struct{})
	}
	if _, ok := f.seen[job.ID]; ok {
		return nil
	}
	f.seen[job.ID] = struct{}{}
	f.jobs = append(f.jobs, jobRecord{ID: job.ID, Type: job.Type, Queue: job.Queue, Payload: job.Payload})
	return nil
}

func (f *fakeEnqueuer) snapshot() []jobRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]jobRecord, len(f.jobs))
	copy(out, f.jobs)
	return out
}
