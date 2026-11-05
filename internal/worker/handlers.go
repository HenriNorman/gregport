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
