package worker

import (
	"context"

	"github.com/hibiken/asynq"

	"github.com/HenriNorman/gregport/internal/queue"
)

// HandlerRegistration pairs an asynq task type with its handler.
type HandlerRegistration struct {
	Type    string
	Handler asynq.Handler
}

// ObserveHandler records one outcome per task execution. Queue retries are
// separate executions, so a task that fails twice appears as two failures.
func ObserveHandler(taskType string, handler asynq.Handler, observe func(taskType, outcome string)) asynq.Handler {
	if handler == nil || observe == nil {
		return handler
	}
	return asynq.HandlerFunc(func(ctx context.Context, task *asynq.Task) error {
		err := handler.ProcessTask(ctx, task)
		if err != nil {
			observe(taskType, "failed")
		} else {
			observe(taskType, "ok")
		}
		return err
	})
}

// NewMux registers task handlers. Handlers are injected so the worker package
// does not need to know how email, SLA breach checks or housekeeping are built.
// system:noop keeps both queue servers exercising the Redis path in the
// meantime.
func NewMux(registrations ...HandlerRegistration) *asynq.ServeMux {
	mux := asynq.NewServeMux()
	mux.HandleFunc(queue.TaskNoop, func(context.Context, *asynq.Task) error {
		return nil
	})
	for _, reg := range registrations {
		if reg.Handler != nil {
			mux.Handle(reg.Type, reg.Handler)
		}
	}
	return mux
}
