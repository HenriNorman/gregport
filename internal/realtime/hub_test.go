package realtime

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

type fakeBackplane struct {
	mu         sync.Mutex
	subs       map[uuid.UUID]map[chan Event]struct{}
	subscribeN int
}

func newFakeBackplane() *fakeBackplane {
	return &fakeBackplane{subs: make(map[uuid.UUID]map[chan Event]struct{})}
}

func (f *fakeBackplane) PublishEvent(_ context.Context, orgID uuid.UUID, ev Event) error {
	f.mu.Lock()
	targets := make([]chan Event, 0, len(f.subs[orgID]))
	for ch := range f.subs[orgID] {
		targets = append(targets, ch)
	}
	f.mu.Unlock()

	for _, ch := range targets {
		select {
		case ch <- ev:
		default:
		}
	}
	return nil
}

func (f *fakeBackplane) Subscribe(_ context.Context, orgID uuid.UUID) (Subscription, error) {
	ch := make(chan Event, 256)
	f.mu.Lock()
	f.subscribeN++
	if f.subs[orgID] == nil {
		f.subs[orgID] = make(map[chan Event]struct{})
	}
	f.subs[orgID][ch] = struct{}{}
	f.mu.Unlock()

	return &fakeSubscription{backplane: f, orgID: orgID, ch: ch}, nil
}

func (f *fakeBackplane) subscriptions() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.subscribeN
}

func (f *fakeBackplane) activeSubscriptions() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	total := 0
	for _, chans := range f.subs {
		total += len(chans)
	}
	return total
}

type fakeSubscription struct {
	backplane *fakeBackplane
	orgID     uuid.UUID
	ch        chan Event
}

func (s *fakeSubscription) Events() <-chan Event { return s.ch }

func (s *fakeSubscription) Close() error {
	s.backplane.mu.Lock()
	delete(s.backplane.subs[s.orgID], s.ch)
	s.backplane.mu.Unlock()
	return nil
}

type fakeReplayer struct {
	mu     sync.Mutex
	events []Event
}

func (r *fakeReplayer) Replay(_ context.Context, orgID uuid.UUID, afterSeq int64, limit int) ([]Event, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]Event, 0, limit)
	for _, ev := range r.events {
		if ev.Seq <= afterSeq || len(out) == limit {
			continue
		}
		out = append(out, ev)
	}
	return out, nil
}

type recordingSink struct {
	mu         sync.Mutex
	events     []Event
	heartbeats int
	started    chan struct{}
	startOnce  sync.Once
	block      chan struct{}
}

func newRecordingSink() *recordingSink {
	return &recordingSink{started: make(chan struct{})}
}

func (s *recordingSink) Event(ev Event) error {
	s.startOnce.Do(func() { close(s.started) })
	if s.block != nil {
		<-s.block
	}
	s.mu.Lock()
	s.events = append(s.events, ev)
	s.mu.Unlock()
	return nil
}

func (s *recordingSink) Heartbeat() error {
	s.mu.Lock()
	s.heartbeats++
	s.mu.Unlock()
	return nil
}

func (s *recordingSink) snapshot() []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Event, len(s.events))
	copy(out, s.events)
	return out
}

func (s *recordingSink) heartbeatCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.heartbeats
}

func waitUntil(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestHubDoesNotCrossOrgBoundary(t *testing.T) {
	backplane := newFakeBackplane()
	hub := NewHub(backplane, nil, nil, HubOptions{Heartbeat: time.Hour, BufferSize: 8})
	defer hub.Close()

	orgA, orgB := uuid.New(), uuid.New()
	ctxA, cancelA := context.WithCancel(context.Background())
	ctxB, cancelB := context.WithCancel(context.Background())
	defer cancelA()
	defer cancelB()

	sinkA, sinkB := newRecordingSink(), newRecordingSink()
	go func() { _ = hub.Serve(ctxA, orgA, 0, sinkA) }()
	go func() { _ = hub.Serve(ctxB, orgB, 0, sinkB) }()
	waitUntil(t, "two connections", func() bool { return hub.Connections() == 2 })

	if err := backplane.PublishEvent(context.Background(), orgA, Event{Seq: 1, Type: "ticket.created"}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	waitUntil(t, "org A event", func() bool { return len(sinkA.snapshot()) == 1 })
	time.Sleep(30 * time.Millisecond)
	if got := len(sinkB.snapshot()); got != 0 {
		t.Fatalf("org B received %d org A events", got)
	}

	if err := backplane.PublishEvent(context.Background(), orgB, Event{Seq: 1, Type: "ticket.created"}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	waitUntil(t, "org B event", func() bool { return len(sinkB.snapshot()) == 1 })
}

func TestHubReplaysMissedThenSwitchesToLive(t *testing.T) {
	replayer := &fakeReplayer{events: []Event{
		{Seq: 1, Type: "ticket.created"},
		{Seq: 2, Type: "ticket.created"},
		{Seq: 3, Type: "ticket.status_changed"},
		{Seq: 4, Type: "comment.added"},
		{Seq: 5, Type: "ticket.assigned"},
	}}
	backplane := newFakeBackplane()
	hub := NewHub(backplane, replayer, nil, HubOptions{Heartbeat: time.Hour, BufferSize: 8})
	defer hub.Close()

	orgID := uuid.New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := newRecordingSink()
	go func() { _ = hub.Serve(ctx, orgID, 2, sink) }()

	waitUntil(t, "replayed events", func() bool { return len(sink.snapshot()) == 3 })
	got := sink.snapshot()
	for i, want := range []int64{3, 4, 5} {
		if got[i].Seq != want {
			t.Fatalf("replayed[%d]: got seq %d, want %d", i, got[i].Seq, want)
		}
	}

	// A duplicate of an already-sent seq is suppressed.
	_ = backplane.PublishEvent(context.Background(), orgID, Event{Seq: 5, Type: "ticket.assigned"})
	// New events arrive live, even if their publish order is not seq order.
	_ = backplane.PublishEvent(context.Background(), orgID, Event{Seq: 7, Type: "ticket.tagged"})
	_ = backplane.PublishEvent(context.Background(), orgID, Event{Seq: 6, Type: "ticket.untagged"})

	waitUntil(t, "live events", func() bool { return len(sink.snapshot()) == 5 })
	got = sink.snapshot()
	if got[3].Seq != 7 || got[4].Seq != 6 {
		t.Fatalf("live order: got %d, %d; want 7, 6", got[3].Seq, got[4].Seq)
	}
}

func TestHubDropsSlowConsumer(t *testing.T) {
	backplane := newFakeBackplane()
	hub := NewHub(backplane, nil, nil, HubOptions{Heartbeat: time.Hour, BufferSize: 1})
	defer hub.Close()

	orgID := uuid.New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sink := newRecordingSink()
	sink.block = make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- hub.Serve(ctx, orgID, 0, sink) }()

	waitUntil(t, "connection registration", func() bool { return hub.Connections() == 1 })
	_ = backplane.PublishEvent(context.Background(), orgID, Event{Seq: 1, Type: "ticket.created"})
	waitUntil(t, "first write", func() bool {
		select {
		case <-sink.started:
			return true
		default:
			return false
		}
	})

	// The writer is blocked, so seq 2 fills the single buffer slot and seq 3
	// proves the fan-out never blocks: it drops the connection instead.
	_ = backplane.PublishEvent(context.Background(), orgID, Event{Seq: 2, Type: "ticket.created"})
	time.Sleep(20 * time.Millisecond)
	_ = backplane.PublishEvent(context.Background(), orgID, Event{Seq: 3, Type: "ticket.created"})

	waitUntil(t, "slow connection drop", func() bool { return hub.Connections() == 0 })
	close(sink.block)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("serve did not return after drop")
	}
}

func TestHubHeartbeatsIdleConnection(t *testing.T) {
	hub := NewHub(newFakeBackplane(), nil, nil, HubOptions{Heartbeat: 10 * time.Millisecond, BufferSize: 4})
	defer hub.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := newRecordingSink()
	go func() { _ = hub.Serve(ctx, uuid.New(), 0, sink) }()

	waitUntil(t, "heartbeat", func() bool { return sink.heartbeatCount() >= 1 })
}

func TestHubUnsubscribesWhenLastConnectionLeaves(t *testing.T) {
	backplane := newFakeBackplane()
	hub := NewHub(backplane, nil, nil, HubOptions{Heartbeat: time.Hour, BufferSize: 4})
	defer hub.Close()

	orgID := uuid.New()
	ctx, cancel := context.WithCancel(context.Background())
	sink := newRecordingSink()
	done := make(chan error, 1)
	go func() { done <- hub.Serve(ctx, orgID, 0, sink) }()

	waitUntil(t, "subscription", func() bool { return backplane.activeSubscriptions() == 1 })
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("serve did not return")
	}
	waitUntil(t, "unsubscribe", func() bool { return backplane.activeSubscriptions() == 0 })
	if hub.Orgs() != 0 {
		t.Fatalf("orgs: %d", hub.Orgs())
	}
}
