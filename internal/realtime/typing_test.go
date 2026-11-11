package realtime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"

	"github.com/HenriNorman/gregport/internal/platform/cache"
)

type fakeChecker struct {
	exists bool
	err    error
}

func (f fakeChecker) TicketExists(context.Context, uuid.UUID, int64) (bool, error) {
	return f.exists, f.err
}

type fakePublisher struct {
	mu     sync.Mutex
	events []Event
	orgID  uuid.UUID
}

func (f *fakePublisher) PublishEvent(_ context.Context, orgID uuid.UUID, ev Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, ev)
	f.orgID = orgID
	return nil
}

func TestTypingSetsKeyWithTTLAndPublishes(t *testing.T) {
	mr := miniredis.RunT(t)
	keys := cache.NewRedis(cache.RedisOptions{Addr: mr.Addr(), Timeout: 50 * time.Millisecond})
	t.Cleanup(func() { _ = keys.Close() })

	publisher := &fakePublisher{}
	service := NewTypingService(fakeChecker{exists: true}, keys, publisher, 5*time.Second)

	orgID, actorID := uuid.New(), uuid.New()
	if err := service.Typing(context.Background(), orgID, 42, actorID); err != nil {
		t.Fatalf("typing: %v", err)
	}

	key := TypingKey(orgID, 42, actorID)
	if !mr.Exists(key) {
		t.Fatal("typing key was not written")
	}
	if ttl := mr.TTL(key); ttl != 5*time.Second {
		t.Fatalf("typing ttl: got %s, want 5s", ttl)
	}

	publisher.mu.Lock()
	defer publisher.mu.Unlock()
	if len(publisher.events) != 1 {
		t.Fatalf("published events: %d", len(publisher.events))
	}
	ev := publisher.events[0]
	if ev.Type != TypeTyping || ev.TicketNumber != 42 || ev.Seq != 0 {
		t.Fatalf("typing event: %+v", ev)
	}
	if ev.ActorID == nil || *ev.ActorID != actorID {
		t.Fatalf("typing actor: %+v", ev.ActorID)
	}
	if publisher.orgID != orgID {
		t.Fatalf("typing org: %s", publisher.orgID)
	}

	mr.FastForward(6 * time.Second)
	if mr.Exists(key) {
		t.Fatal("typing key did not expire")
	}
}

func TestTypingUnknownTicketDoesNotPublish(t *testing.T) {
	mr := miniredis.RunT(t)
	keys := cache.NewRedis(cache.RedisOptions{Addr: mr.Addr(), Timeout: 50 * time.Millisecond})
	t.Cleanup(func() { _ = keys.Close() })

	publisher := &fakePublisher{}
	service := NewTypingService(fakeChecker{exists: false}, keys, publisher, 5*time.Second)

	err := service.Typing(context.Background(), uuid.New(), 99, uuid.New())
	if !errors.Is(err, ErrTicketNotFound) {
		t.Fatalf("got %v, want ErrTicketNotFound", err)
	}
	if len(publisher.events) != 0 {
		t.Fatalf("published %d events", len(publisher.events))
	}
}

func TestTypingKeyIsOrgAndActorScoped(t *testing.T) {
	orgA, orgB := uuid.New(), uuid.New()
	actorA, actorB := uuid.New(), uuid.New()

	a := TypingKey(orgA, 1, actorA)
	b := TypingKey(orgA, 1, actorB)
	c := TypingKey(orgB, 1, actorA)
	if a == b || a == c || b == c {
		t.Fatalf("typing keys are not distinct: %q %q %q", a, b, c)
	}
}
