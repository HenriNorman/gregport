package realtime

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// Publisher sends events to the Redis backplane. The outbox dispatcher and the
// typing endpoint only need this half of the backplane.
type Publisher interface {
	PublishEvent(ctx context.Context, orgID uuid.UUID, ev Event) error
}

// Subscription is one org's live event stream.
type Subscription interface {
	Events() <-chan Event
	Close() error
}

// Backplane moves events between API instances. Subscriptions are only opened
// for orgs that have at least one local SSE connection.
type Backplane interface {
	Publisher
	Subscribe(ctx context.Context, orgID uuid.UUID) (Subscription, error)
}

// RedisBackplane implements Backplane with redis/go-redis pub/sub.
type RedisBackplane struct {
	client *redis.Client
}

func NewRedisBackplane(client *redis.Client) *RedisBackplane {
	return &RedisBackplane{client: client}
}

func (b *RedisBackplane) PublishEvent(ctx context.Context, orgID uuid.UUID, ev Event) error {
	if ev.Data == nil {
		ev.Data = json.RawMessage(`{}`)
	}
	payload, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("marshal realtime event: %w", err)
	}
	if err := b.client.Publish(ctx, OrgChannel(orgID), payload).Err(); err != nil {
		return fmt.Errorf("publish realtime event: %w", err)
	}
	return nil
}

// Subscribe blocks until Redis confirms the subscription, so the caller can
// run the replay query afterwards without a gap.
func (b *RedisBackplane) Subscribe(ctx context.Context, orgID uuid.UUID) (Subscription, error) {
	pubsub := b.client.Subscribe(ctx, OrgChannel(orgID))
	if _, err := pubsub.Receive(ctx); err != nil {
		_ = pubsub.Close()
		return nil, fmt.Errorf("subscribe %s: %w", OrgChannel(orgID), err)
	}

	events := make(chan Event, 64)
	go func() {
		defer close(events)
		defer pubsub.Close()

		for {
			msg, err := pubsub.ReceiveMessage(ctx)
			if err != nil {
				return
			}
			var ev Event
			if err := json.Unmarshal([]byte(msg.Payload), &ev); err != nil {
				continue
			}
			select {
			case events <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()

	return &redisSubscription{events: events, pubsub: pubsub}, nil
}

type redisSubscription struct {
	events <-chan Event
	pubsub *redis.PubSub
}

func (s *redisSubscription) Events() <-chan Event { return s.events }

func (s *redisSubscription) Close() error { return s.pubsub.Close() }
