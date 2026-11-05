package worker

import (
	"context"

	"github.com/hibiken/asynq"

	"github.com/HenriNorman/gregport/internal/queue"
)

// NewMux registers task handlers. Real consumers are added as their packages
// land; system:noop keeps both queue servers exercising the Redis path in the
// meantime.
func NewMux() *asynq.ServeMux {
	mux := asynq.NewServeMux()
	mux.HandleFunc(queue.TaskNoop, func(context.Context, *asynq.Task) error {
		return nil
	})
	return mux
}
