package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"

	"github.com/HenriNorman/gregport/internal/queue"
)

func TestHandlerProcessTaskFanOutAndReplay(t *testing.T) {
	ctx := context.Background()

	orgID := uuid.New()
	ticketID := uuid.New()
	commentID := uuid.New()
	eventID := uuid.New()
	assigneeID := uuid.New()
	watcherID := uuid.New()
	actorID := uuid.New()

	repo := &fakeRepo{
		ticketCtx: TicketContext{
			OrgID:      orgID,
			TicketID:   ticketID,
			Number:     1042,
			Subject:    "handler ticket",
			AssigneeID: &assigneeID,
		},
		commentCtx: &CommentContext{
			ID:         commentID,
			TicketID:   ticketID,
			AuthorType: "customer",
			Body:       "hello",
		},
		watchers: []uuid.UUID{watcherID},
	}
	svc := NewService(repo)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := NewTaskHandler(svc, repo, logger)

	data, _ := json.Marshal(map[string]any{
		"comment_id":  commentID.String(),
		"is_internal": false,
	})
	payload, _ := json.Marshal(TaskPayload{
		EventID:   eventID,
		OrgID:     orgID,
		TicketID:  ticketID,
		EventType: "comment.added",
		ActorID:   actorID,
		Data:      data,
	})
	task := asynq.NewTask(queue.TaskNotify, payload)

	if err := handler.ProcessTask(ctx, task); err != nil {
		t.Fatalf("process task: %v", err)
	}

	// Assignee and watcher get one notification each; the actor is excluded.
	byUser := repo.notifications[eventID]
	if len(byUser) != 2 {
		t.Fatalf("inserted %d notifications, want 2: %+v", len(byUser), byUser)
	}
	if _, ok := byUser[actorID]; ok {
		t.Fatal("actor received a notification")
	}
	if _, ok := byUser[assigneeID]; !ok {
		t.Fatal("assignee did not receive a notification")
	}
	if _, ok := byUser[watcherID]; !ok {
		t.Fatal("watcher did not receive a notification")
	}

	// Replaying the same job inserts no duplicates.
	if err := handler.ProcessTask(ctx, task); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if got := len(repo.notifications[eventID]); got != 2 {
		t.Fatalf("after replay: %d notifications, want 2", got)
	}
}

func TestHandlerProcessTaskIgnoresUnknownEvent(t *testing.T) {
	repo := &fakeRepo{}
	svc := NewService(repo)
	handler := NewTaskHandler(svc, repo, slog.New(slog.NewTextHandler(io.Discard, nil)))

	payload, _ := json.Marshal(TaskPayload{
		EventID:   uuid.New(),
		OrgID:     uuid.New(),
		TicketID:  uuid.New(),
		EventType: "ticket.updated",
	})
	if err := handler.ProcessTask(context.Background(), asynq.NewTask(queue.TaskNotify, payload)); err != nil {
		t.Fatalf("unknown event should be a no-op: %v", err)
	}
	if len(repo.notifications) != 0 {
		t.Fatalf("unexpected notifications: %+v", repo.notifications)
	}
}

func TestHandlerProcessTaskRejectsMalformedPayload(t *testing.T) {
	repo := &fakeRepo{}
	svc := NewService(repo)
	handler := NewTaskHandler(svc, repo, slog.New(slog.NewTextHandler(io.Discard, nil)))

	err := handler.ProcessTask(context.Background(), asynq.NewTask(queue.TaskNotify, []byte(`{`)))
	if err == nil {
		t.Fatal("expected malformed payload to fail")
	}
	if !errors.Is(err, asynq.SkipRetry) {
		t.Fatalf("malformed payload should be permanent, got %v", err)
	}
}
