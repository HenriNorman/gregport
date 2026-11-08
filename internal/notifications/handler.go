// Package notify provides the asynq handler for notify:send tasks.
package notifications

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

// eventTypeToNotify maps outbox event types to notification types.
var eventTypeToNotify = map[string]string{
	"ticket.created":  EventTicketCreated,
	"comment.added":   EventNewComment,
	"ticket.assigned": EventTicketAssigned,
	"sla.breached":    EventSLABreach,
}

// TaskHandler consumes notify:send tasks.
type TaskHandler struct {
	service *Service
	repo    Repo
	logger  *slog.Logger
}

var _ asynq.Handler = (*TaskHandler)(nil)

// NewTaskHandler builds a notify task handler.
func NewTaskHandler(service *Service, repo Repo, logger *slog.Logger) *TaskHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &TaskHandler{service: service, repo: repo, logger: logger}
}

// ProcessTask decodes the event payload, computes recipients, applies
// preferences, and inserts notification rows.
func (h *TaskHandler) ProcessTask(ctx context.Context, task *asynq.Task) error {
	var p TaskPayload
	if err := json.Unmarshal(task.Payload(), &p); err != nil {
		return queue.Permanent(fmt.Errorf("decode notify task: %w", err))
	}
	if err := p.Validate(); err != nil {
		return queue.Permanent(err)
	}

	notifyType, ok := eventTypeToNotify[p.EventType]
	if !ok {
		h.logger.Debug("event type has no notification mapping",
			"event_id", p.EventID,
			"event_type", p.EventType,
		)
		return nil
	}

	tc, err := h.repo.LoadTicketContext(ctx, p.OrgID, p.TicketID)
	if err != nil {
		if errors.Is(err, ErrTicketNotFound) {
			h.logger.Warn("notify skipped; ticket no longer exists",
				"event_id", p.EventID,
				"ticket_id", p.TicketID,
			)
			return nil
		}
		return fmt.Errorf("load ticket context for event %s: %w", p.EventID, err)
	}

	var commentCtx *CommentContext
	if p.EventType == "comment.added" {
		var data struct {
			CommentID  string `json:"comment_id"`
			IsInternal bool   `json:"is_internal"`
		}
		if err := json.Unmarshal(p.Data, &data); err != nil {
			return queue.Permanent(fmt.Errorf("decode comment event data: %w", err))
		}
		commentID, err := uuid.Parse(data.CommentID)
		if err != nil {
			return queue.Permanent(fmt.Errorf("parse comment id: %w", err))
		}
		c, err := h.repo.LoadComment(ctx, p.OrgID, commentID)
		if err != nil {
			if errors.Is(err, ErrCommentNotFound) {
				h.logger.Warn("notify skipped; comment no longer exists",
					"event_id", p.EventID,
					"comment_id", commentID,
				)
				return nil
			}
			return fmt.Errorf("load comment for event %s: %w", p.EventID, err)
		}
		commentCtx = &c
	}

	watchers, err := h.repo.ListWatchers(ctx, p.TicketID)
	if err != nil {
		return fmt.Errorf("list watchers for event %s: %w", p.EventID, err)
	}

	n, err := h.service.FanOut(
		ctx, p.OrgID, p.EventID, notifyType, tc, commentCtx, watchers, p.ActorID,
	)
	if err != nil {
		return fmt.Errorf("fan out notifications for event %s: %w", p.EventID, err)
	}

	h.logger.Info("notifications fanned out",
		"event_id", p.EventID,
		"event_type", p.EventType,
		"notify_type", notifyType,
		"inserted", n,
	)
	return nil
}
