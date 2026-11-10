// Package outbox turns undispatched ticket_events rows into queue jobs.
//
// Delivery is at-least-once: Redis and PostgreSQL cannot be updated in one
// atomic step, so a crash after enqueueing but before committing can produce a
// duplicate. Every consumer therefore checks whether its effect already
// exists and treats a duplicate as success.
package outbox

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/HenriNorman/gregport/internal/platform/database"
	"github.com/HenriNorman/gregport/internal/queue"
)

// Consumer names are part of the deterministic task ID.
const (
	ConsumerEmail   = "email"
	ConsumerNotify  = "notify"
	ConsumerWebhook = "webhook"
	ConsumerSearch  = "search"
)

// searchConsumer refreshes one ticket document in the derived search index.
// It uses the low-priority queue so a large indexing backlog cannot delay
// customer email or notifications, and retries generously because a failure
// only leaves the index temporarily stale.
func searchConsumer() Consumer {
	return Consumer{
		Name:     ConsumerSearch,
		TaskType: queue.TaskSearchIndex,
		Queue:    queue.QueueLow,
		MaxRetry: 10,
	}
}

// Consumer describes one downstream job for an event type.
type Consumer struct {
	Name     string
	TaskType string
	Queue    string
	MaxRetry int
}

// consumers maps an event type to its downstream consumers. Only implemented
// consumers are listed: enqueueing a task without a handler would retry and
// archive forever.
//
// Every event that can change a ticket's indexed fields (subject, comments,
// status, priority, tags, assignee, requester email, deletion) includes the
// search consumer. SLA breach is excluded because it changes neither the ticket
// row nor its comments; the notification and webhook consumers still run.
var consumers = map[string][]Consumer{
	"ticket.created": {
		{Name: ConsumerEmail, TaskType: queue.TaskEmailSend, Queue: queue.QueueCritical, MaxRetry: 10},
		{Name: ConsumerNotify, TaskType: queue.TaskNotify, Queue: queue.QueueCritical, MaxRetry: 5},
		{Name: ConsumerWebhook, TaskType: queue.TaskWebhookDispatch, Queue: queue.QueueDefault, MaxRetry: 5},
		searchConsumer(),
	},
	"comment.added": {
		{Name: ConsumerEmail, TaskType: queue.TaskEmailSend, Queue: queue.QueueCritical, MaxRetry: 10},
		{Name: ConsumerNotify, TaskType: queue.TaskNotify, Queue: queue.QueueCritical, MaxRetry: 5},
		{Name: ConsumerWebhook, TaskType: queue.TaskWebhookDispatch, Queue: queue.QueueDefault, MaxRetry: 5},
		searchConsumer(),
	},
	"ticket.assigned": {
		{Name: ConsumerEmail, TaskType: queue.TaskEmailSend, Queue: queue.QueueCritical, MaxRetry: 10},
		{Name: ConsumerNotify, TaskType: queue.TaskNotify, Queue: queue.QueueCritical, MaxRetry: 5},
		{Name: ConsumerWebhook, TaskType: queue.TaskWebhookDispatch, Queue: queue.QueueDefault, MaxRetry: 5},
		searchConsumer(),
	},
	"sla.breached": {
		{Name: ConsumerEmail, TaskType: queue.TaskEmailSend, Queue: queue.QueueCritical, MaxRetry: 10},
		{Name: ConsumerNotify, TaskType: queue.TaskNotify, Queue: queue.QueueCritical, MaxRetry: 5},
		{Name: ConsumerWebhook, TaskType: queue.TaskWebhookDispatch, Queue: queue.QueueDefault, MaxRetry: 5},
	},
	// A status change is only interesting to the customer when it solved the
	// ticket: that email carries the satisfaction-rating link. Every other
	// transition is filtered out for the email consumer only; webhooks receive
	// every status transition.
	"ticket.status_changed": {
		{Name: ConsumerEmail, TaskType: queue.TaskEmailSend, Queue: queue.QueueCritical, MaxRetry: 10},
		{Name: ConsumerWebhook, TaskType: queue.TaskWebhookDispatch, Queue: queue.QueueDefault, MaxRetry: 5},
		searchConsumer(),
	},
	"ticket.closed": {
		{Name: ConsumerWebhook, TaskType: queue.TaskWebhookDispatch, Queue: queue.QueueDefault, MaxRetry: 5},
		searchConsumer(),
	},
	"ticket.deleted": {
		{Name: ConsumerWebhook, TaskType: queue.TaskWebhookDispatch, Queue: queue.QueueDefault, MaxRetry: 5},
		searchConsumer(),
	},
	"ticket.merged": {
		{Name: ConsumerWebhook, TaskType: queue.TaskWebhookDispatch, Queue: queue.QueueDefault, MaxRetry: 5},
		searchConsumer(),
	},
	// Merging appends a system comment to the target ticket without changing
	// the source's indexed fields further. This internal event exists solely
	// to refresh the target document; it is not exposed to webhooks.
	"ticket.merge_target_updated": {
		searchConsumer(),
	},
	"ticket.tagged": {
		{Name: ConsumerWebhook, TaskType: queue.TaskWebhookDispatch, Queue: queue.QueueDefault, MaxRetry: 5},
		searchConsumer(),
	},
	"ticket.untagged": {
		{Name: ConsumerWebhook, TaskType: queue.TaskWebhookDispatch, Queue: queue.QueueDefault, MaxRetry: 5},
		searchConsumer(),
	},
}

// ConsumersFor returns the downstream jobs for an event. Unknown event types
// have no consumers; their rows are still marked dispatched so they do not
// pollute every poll. Payload-level filtering is applied per consumer, so a
// non-solved status change still reaches webhooks even though it produces no
// customer email.
func ConsumersFor(ev Event) []Consumer {
	all := consumers[ev.EventType]
	if len(all) == 0 {
		return nil
	}
	out := make([]Consumer, 0, len(all))
	for _, consumer := range all {
		if consumer.Name == ConsumerEmail && !emailEventDataAllows(ev) {
			continue
		}
		out = append(out, consumer)
	}
	return out
}

// emailEventDataAllows applies the payload-level filter for event types whose
// email consumer only handles a subset of transitions.
func emailEventDataAllows(ev Event) bool {
	if ev.EventType != "ticket.status_changed" {
		return true
	}
	var body struct {
		New string `json:"new"`
	}
	if len(ev.Data) == 0 {
		return false
	}
	if err := json.Unmarshal(ev.Data, &body); err != nil {
		return false
	}
	return body.New == "solved"
}

// Event is one claimed outbox row.
type Event struct {
	ID        uuid.UUID
	OrgID     uuid.UUID
	TicketID  uuid.UUID
	EventType string
	ActorID   uuid.UUID
	Data      []byte
	CreatedAt time.Time
}

// EventJob is the JSON payload handed to every consumer.
type EventJob struct {
	EventID   uuid.UUID       `json:"event_id"`
	OrgID     uuid.UUID       `json:"org_id"`
	TicketID  uuid.UUID       `json:"ticket_id"`
	EventType string          `json:"event_type"`
	ActorID   uuid.UUID       `json:"actor_id"`
	Data      json.RawMessage `json:"data"`
}

// Enqueuer is the queue side of the dispatcher. queue.Client satisfies it.
type Enqueuer interface {
	EnqueueJob(ctx context.Context, job queue.Job) error
}

// Dispatcher drains the outbox in bounded batches.
type Dispatcher struct {
	pool     *pgxpool.Pool
	enqueuer Enqueuer
	logger   *slog.Logger
	batch    int
	interval time.Duration
}

// NewDispatcher creates a dispatcher. Batch and interval fall back to safe
// defaults when non-positive.
func NewDispatcher(pool *pgxpool.Pool, enqueuer Enqueuer, logger *slog.Logger, batch int, interval time.Duration) *Dispatcher {
	if batch <= 0 {
		batch = 100
	}
	if interval <= 0 {
		interval = time.Second
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Dispatcher{
		pool:     pool,
		enqueuer: enqueuer,
		logger:   logger,
		batch:    batch,
		interval: interval,
	}
}

// Run polls until ctx is cancelled. Errors are logged and retried on the next
// tick; a transient database failure must not stop the worker.
func (d *Dispatcher) Run(ctx context.Context) {
	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()

	for {
		n, err := d.DispatchOnce(ctx)
		if err != nil && ctx.Err() == nil {
			d.logger.Error("outbox dispatch failed", "error", err)
		} else if n > 0 {
			d.logger.Debug("outbox dispatched", "events", n)
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// DispatchOnce drains batches until a batch is not full, so a backlog is
// cleared without waiting one interval per batch.
func (d *Dispatcher) DispatchOnce(ctx context.Context) (int, error) {
	total := 0
	for {
		n, err := d.dispatchBatch(ctx)
		if err != nil {
			return total, err
		}
		total += n
		if n < d.batch {
			return total, nil
		}
	}
}

func (d *Dispatcher) dispatchBatch(ctx context.Context) (int, error) {
	events, err := d.claimAndEnqueue(ctx)
	if err != nil {
		return 0, err
	}
	return len(events), nil
}

// claimAndEnqueue runs the claim, enqueue and mark in one PostgreSQL
// transaction. If enqueueing fails the transaction rolls back, so the events
// remain undispatched and will be retried. Some jobs may already have been
// enqueued, which is the accepted duplicate window.
func (d *Dispatcher) claimAndEnqueue(ctx context.Context) ([]Event, error) {
	var claimed []Event

	err := database.WithTx(ctx, d.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT id, org_id, ticket_id, event_type, actor_id, data, created_at
			 FROM ticket_events
			 WHERE dispatched_at IS NULL
			 ORDER BY created_at, id
			 LIMIT $1
			 FOR UPDATE SKIP LOCKED`,
			d.batch,
		)
		if err != nil {
			return fmt.Errorf("claim outbox rows: %w", err)
		}

		claimed = claimed[:0]
		for rows.Next() {
			var ev Event
			if err := rows.Scan(&ev.ID, &ev.OrgID, &ev.TicketID, &ev.EventType, &ev.ActorID, &ev.Data, &ev.CreatedAt); err != nil {
				rows.Close()
				return fmt.Errorf("scan outbox row: %w", err)
			}
			claimed = append(claimed, ev)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return fmt.Errorf("iterate outbox rows: %w", err)
		}
		rows.Close()

		for _, ev := range claimed {
			for _, consumer := range ConsumersFor(ev) {
				job, err := jobFor(ev, consumer)
				if err != nil {
					return err
				}
				if err := d.enqueuer.EnqueueJob(ctx, job); err != nil {
					return fmt.Errorf("enqueue %s for event %s: %w", consumer.Name, ev.ID, err)
				}
			}

			if _, err := tx.Exec(ctx,
				`UPDATE ticket_events SET dispatched_at = now() WHERE id = $1`,
				ev.ID,
			); err != nil {
				return fmt.Errorf("mark event %s dispatched: %w", ev.ID, err)
			}
		}

		return nil
	})
	if err != nil {
		return nil, err
	}
	return claimed, nil
}

func jobFor(ev Event, consumer Consumer) (queue.Job, error) {
	payload, err := json.Marshal(EventJob{
		EventID:   ev.ID,
		OrgID:     ev.OrgID,
		TicketID:  ev.TicketID,
		EventType: ev.EventType,
		ActorID:   ev.ActorID,
		Data:      json.RawMessage(ev.Data),
	})
	if err != nil {
		return queue.Job{}, fmt.Errorf("marshal event job: %w", err)
	}

	return queue.Job{
		ID:       ev.ID.String() + ":" + consumer.Name,
		Type:     consumer.TaskType,
		Queue:    consumer.Queue,
		Payload:  payload,
		MaxRetry: consumer.MaxRetry,
	}, nil
}
