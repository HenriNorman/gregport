package realtime

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// ErrTicketNotFound is returned for a ticket outside the caller's org, matching
// the rest of the API: another tenant's ticket is 404, never 403.
var ErrTicketNotFound = errors.New("realtime: ticket not found")

// KeySetter is the cache write used by the typing indicator. cache.Redis
// satisfies it.
type KeySetter interface {
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
}

// TicketChecker verifies that a ticket number belongs to the org before an
// ephemeral typing event is broadcast.
type TicketChecker interface {
	TicketExists(ctx context.Context, orgID uuid.UUID, number int64) (bool, error)
}

// TypingService writes a short-lived Redis key and publishes a typing event.
// There is no "stopped typing" message: clients treat the absence of a fresh
// event for five seconds as stopped.
type TypingService struct {
	tickets   TicketChecker
	keys      KeySetter
	publisher Publisher
	ttl       time.Duration
}

func NewTypingService(tickets TicketChecker, keys KeySetter, publisher Publisher, ttl time.Duration) *TypingService {
	if ttl <= 0 {
		ttl = 5 * time.Second
	}
	return &TypingService{tickets: tickets, keys: keys, publisher: publisher, ttl: ttl}
}

// TypingKey is org-scoped and includes the actor so several agents can type on
// the same ticket independently.
func TypingKey(orgID uuid.UUID, number int64, actorID uuid.UUID) string {
	return fmt.Sprintf("realtime:typing:%s:%d:%s", orgID, number, actorID)
}

func (s *TypingService) Typing(ctx context.Context, orgID uuid.UUID, number int64, actorID uuid.UUID) error {
	exists, err := s.tickets.TicketExists(ctx, orgID, number)
	if err != nil {
		return err
	}
	if !exists {
		return ErrTicketNotFound
	}

	if err := s.keys.Set(ctx, TypingKey(orgID, number, actorID), []byte("1"), s.ttl); err != nil {
		return fmt.Errorf("store typing indicator: %w", err)
	}

	ev := Event{
		Type:         TypeTyping,
		TicketNumber: number,
		Data:         []byte(`{}`),
	}
	if actorID != uuid.Nil {
		ev.ActorID = &actorID
	}
	if err := s.publisher.PublishEvent(ctx, orgID, ev); err != nil {
		return err
	}
	return nil
}
