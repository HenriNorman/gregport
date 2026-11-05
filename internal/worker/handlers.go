package worker

import (
	"context"

	"github.com/hibiken/asynq"

	"github.com/HenriNorman/gregport/internal/queue"
)

// NewMux registers task handlers. Handlers are injected so the worker package
// does not need to know how email, SLA checks or housekeeping are built.
// system:noop keeps both queue servers exercising the Redis path.
func NewMux(emailHandler asynq.Handler) *asynq.ServeMux {
	mux := asynq.NewServeMux()
	mux.HandleFunc(queue.TaskNoop, func(context.Context, *asynq.Task) error {
		return nil
	})
	if emailHandler != nil {
		mux.Handle(queue.TaskEmailSend, emailHandler)
	}
	return mux
}
