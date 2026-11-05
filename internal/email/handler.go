package email

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/hibiken/asynq"

	"github.com/HenriNorman/gregport/internal/queue"
)

// maxDeliveryAttempts is how many times a single delivery is retried before it
// is marked dead. Provider outages and transient SMTP failures are expected;
// a delivery that keeps failing is dead-lettered instead of retried forever.
const maxDeliveryAttempts = 5

// TaskHandler consumes email:send tasks. Planning is idempotent, so a
// duplicate task created by the at-least-once outbox converges on the existing
// delivery rows; already-sent deliveries are skipped.
type TaskHandler struct {
	service *Service
	sender  Sender
	logger  *slog.Logger
}

var _ asynq.Handler = (*TaskHandler)(nil)

// NewTaskHandler builds an email task handler. sender may be nil in tests;
// a nil sender means no delivery is attempted (planning only).
func NewTaskHandler(service *Service, sender Sender, logger *slog.Logger) *TaskHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &TaskHandler{service: service, sender: sender, logger: logger}
}

// ProcessTask decodes the event payload, plans pending deliveries, and sends
// each one that is not already sent or dead. A malformed payload is permanent
// (retrying cannot succeed). Missing ticket/comment rows are terminal.
func (h *TaskHandler) ProcessTask(ctx context.Context, task *asynq.Task) error {
	var p TaskPayload
	if err := json.Unmarshal(task.Payload(), &p); err != nil {
		return queue.Permanent(fmt.Errorf("decode email task: %w", err))
	}
	if err := p.Validate(); err != nil {
		return queue.Permanent(err)
	}

	deliveries, err := h.service.Plan(ctx, p)
	if err != nil {
		switch {
		case errors.Is(err, ErrTicketNotFound), errors.Is(err, ErrCommentNotFound):
			h.logger.Warn("email delivery skipped; referenced row no longer exists",
				"event_id", p.EventID,
				"event_type", p.EventType,
				"error", err,
			)
			return nil
		default:
			return fmt.Errorf("plan email for event %s: %w", p.EventID, err)
		}
	}

	if len(deliveries) == 0 {
		h.logger.Debug("email event has no deliverable recipient",
			"event_id", p.EventID,
			"event_type", p.EventType,
		)
		return nil
	}

	for _, d := range deliveries {
		switch d.Status {
		case StatusSent:
			h.logger.Debug("email already sent, skipping",
				"event_id", p.EventID, "delivery_id", d.ID,
			)
			continue
		case StatusDead:
			h.logger.Debug("email already dead, skipping",
				"event_id", p.EventID, "delivery_id", d.ID,
			)
			continue
		}

		if err := h.sendOne(ctx, p, d); err != nil {
			// Log but continue: one recipient's failure must not cancel the
			// rest of the same event.
			h.logger.Error("email delivery failed",
				"event_id", p.EventID,
				"delivery_id", d.ID,
				"to", d.ToEmail,
				"template", d.Template,
				"error", err,
			)
		}
	}

	h.logger.Info("email deliveries processed",
		"event_id", p.EventID,
		"event_type", p.EventType,
		"deliveries", len(deliveries),
	)
	return nil
}

// sendOne attempts one delivery through the circuit breaker and records the
// outcome. A circuit-open error is treated as a transient provider outage;
// a sender error counts as an attempt.
func (h *TaskHandler) sendOne(ctx context.Context, p TaskPayload, d Delivery) error {
	if h.sender == nil {
		return nil
	}

	payload := d.Payload
	var parsed DeliveryPayload
	if err := json.Unmarshal(payload, &parsed); err != nil {
		parsed = DeliveryPayload{EventType: p.EventType, EventData: payload}
	}

	err := h.sender.Send(ctx, d.ToEmail, d.Template, parsed)
	if err != nil {
		return h.recordFailure(ctx, d, err)
	}

	if err := h.service.repo.MarkSent(ctx, d.ID); err != nil {
		return fmt.Errorf("mark sent: %w", err)
	}
	return nil
}

func (h *TaskHandler) recordFailure(ctx context.Context, d Delivery, err error) error {
	nextAttempt := d.Attempts + 1
	var nextRetry *time.Time
	var lastError *string
	errMsg := err.Error()
	lastError = &errMsg

	if nextAttempt >= maxDeliveryAttempts {
		nextRetry = nil
		if err := h.service.repo.RecordAttempt(ctx, d.ID, StatusDead, nextAttempt, nil, lastError); err != nil {
			return fmt.Errorf("mark dead: %w", err)
		}
		h.logger.Warn("email delivery dead after max attempts",
			"delivery_id", d.ID, "to", d.ToEmail, "attempts", nextAttempt,
		)
		return nil
	}

	// Exponential backoff: 1m, 2m, 4m, 8m.
	backoff := time.Minute * (1 << (nextAttempt - 1))
	if backoff > time.Hour {
		backoff = time.Hour
	}
	now := time.Now()
	retryAt := now.Add(backoff).Round(time.Second)
	nextRetry = &retryAt

	if err := h.service.repo.RecordAttempt(ctx, d.ID, StatusFailed, nextAttempt, nextRetry, lastError); err != nil {
		return fmt.Errorf("mark failed: %w", err)
	}
	return nil
}
