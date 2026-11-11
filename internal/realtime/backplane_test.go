package realtime

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// Two hubs share one Redis, which is what happens when a proxy balances
// clients across API instances: a write handled by either instance reaches
// every instance with a local connection.
func TestRedisBackplaneDeliversAcrossInstances(t *testing.T) {
	mr := miniredis.RunT(t)

	client := func() *redis.Client {
		c := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		t.Cleanup(func() { _ = c.Close() })
		return c
	}

	orgID := uuid.New()
	hubA := NewHub(NewRedisBackplane(client()), nil, nil, HubOptions{Heartbeat: time.Hour, BufferSize: 8})
	hubB := NewHub(NewRedisBackplane(client()), nil, nil, HubOptions{Heartbeat: time.Hour, BufferSize: 8})
	defer hubA.Close()
	defer hubB.Close()

	ctxA, cancelA := context.WithCancel(context.Background())
	ctxB, cancelB := context.WithCancel(context.Background())
	defer cancelA()
	defer cancelB()

	sinkA, sinkB := newRecordingSink(), newRecordingSink()
	go func() { _ = hubA.Serve(ctxA, orgID, 0, sinkA) }()
	go func() { _ = hubB.Serve(ctxB, orgID, 0, sinkB) }()
	waitUntil(t, "both instances subscribed", func() bool {
		return hubA.Orgs() == 1 && hubB.Orgs() == 1
	})

	publisher := NewRedisBackplane(client())
	if err := publisher.PublishEvent(context.Background(), orgID, Event{
		Seq:          9,
		Type:         "ticket.status_changed",
		TicketNumber: 42,
		Data:         []byte(`{"new":"solved"}`),
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	waitUntil(t, "instance A delivery", func() bool { return len(sinkA.snapshot()) == 1 })
	waitUntil(t, "instance B delivery", func() bool { return len(sinkB.snapshot()) == 1 })
	if got := sinkB.snapshot()[0].Seq; got != 9 {
		t.Fatalf("delivered seq: %d", got)
	}

	// Typing events travel on the same channel with no seq.
	if err := publisher.PublishEvent(context.Background(), orgID, Event{Type: TypeTyping, TicketNumber: 42}); err != nil {
		t.Fatalf("publish typing: %v", err)
	}
	waitUntil(t, "typing delivery", func() bool { return len(sinkA.snapshot()) == 2 })
	if ev := sinkA.snapshot()[1]; ev.Type != TypeTyping || ev.Seq != 0 {
		t.Fatalf("typing event: %+v", ev)
	}
}
