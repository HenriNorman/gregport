package webhooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"

	"github.com/HenriNorman/gregport/internal/queue"
)

// TaskHandler consumes webhook:dispatch and webhook:deliver tasks.
type TaskHandler struct {
	service *Service
	logger  *slog.Logger
}

// NewTaskHandler builds the worker-side webhook handler.
func NewTaskHandler(service *Service, logger *slog.Logger) *TaskHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &TaskHandler{service: service, logger: logger}
}

// Dispatch plans deliveries for one outbox event and enqueues their attempts.
func (h *TaskHandler) Dispatch(ctx context.Context, task *asynq.Task) error {
	var event OutboxEvent
	if err := json.Unmarshal(task.Payload(), &event); err != nil {
		return queue.Permanent(fmt.Errorf("decode webhook dispatch task: %w", err))
	}
	if err := event.Validate(); err != nil {
		return queue.Permanent(err)
	}

	if err := h.service.Dispatch(ctx, event); err != nil {
		return fmt.Errorf("dispatch webhooks for event %s: %w", event.EventID, err)
	}
	h.logger.Info("webhook deliveries planned",
		"event_id", event.EventID,
		"event_type", event.EventType,
	)
	return nil
}

// Deliver performs one delivery attempt. HTTP failures are returned so asynq
// applies the webhook retry schedule; a delivery that reached its terminal
// attempt is stored as dead and returns nil instead of being retried again.
func (h *TaskHandler) Deliver(ctx context.Context, task *asynq.Task) error {
	var payload DeliveryTaskPayload
	if err := json.Unmarshal(task.Payload(), &payload); err != nil {
		return queue.Permanent(fmt.Errorf("decode webhook delivery task: %w", err))
	}
	if payload.DeliveryID == uuid.Nil {
		return queue.Permanent(errors.New("webhooks: delivery_id is required"))
	}

	if err := h.service.Deliver(ctx, payload.DeliveryID); err != nil {
		if errors.Is(err, ErrNoSender) {
			return queue.Permanent(err)
		}
		return err
	}
	return nil
}
