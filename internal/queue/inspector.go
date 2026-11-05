package queue

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hibiken/asynq"
)

const (
	defaultDeadLetterPageSize = 30
	maxDeadLetterPageSize     = 100
)

// Inspector reads queue state without consuming tasks. It is the programmatic
// counterpart of Asynqmon and is used for queue statistics logging.
type Inspector struct {
	inner *asynq.Inspector
}

// NewInspector creates an inspector for the same Redis database as the queue.
func NewInspector(cfg Config) *Inspector {
	return &Inspector{inner: asynq.NewInspector(redisOpt(cfg.Redis))}
}

// Close releases the Redis connection.
func (i *Inspector) Close() error {
	return i.inner.Close()
}

// QueueStats is a point-in-time snapshot of one queue. Archived counts the
// dead-letter set: tasks that exhausted their retries or were marked
// permanent.
type QueueStats struct {
	Name        string
	Size        int
	Pending     int
	Active      int
	Scheduled   int
	Retry       int
	Archived    int
	Completed   int
	Paused      bool
	Latency     time.Duration
	MemoryUsage int64
	Processed   int
	Failed      int
}

// Queues lists every queue that currently has state in Redis.
func (i *Inspector) Queues(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	names, err := i.inner.Queues()
	if err != nil {
		return nil, fmt.Errorf("list queues: %w", err)
	}
	return names, nil
}

// Stats snapshots the given queues. When queues is empty, every known queue is
// inspected. Queues without current state are skipped, so a concurrent cleanup
// or an idle queue cannot fail the whole snapshot.
func (i *Inspector) Stats(ctx context.Context, queues []string) ([]QueueStats, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	names := queues
	if len(names) == 0 {
		var err error
		names, err = i.Queues(ctx)
		if err != nil {
			return nil, err
		}
	}

	out := make([]QueueStats, 0, len(names))
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		info, err := i.inner.GetQueueInfo(name)
		if err != nil {
			if errors.Is(err, asynq.ErrQueueNotFound) {
				continue
			}
			return nil, fmt.Errorf("queue %q info: %w", name, err)
		}
		out = append(out, QueueStats{
			Name:        info.Queue,
			Size:        info.Size,
			Pending:     info.Pending,
			Active:      info.Active,
			Scheduled:   info.Scheduled,
			Retry:       info.Retry,
			Archived:    info.Archived,
			Completed:   info.Completed,
			Paused:      info.Paused,
			Latency:     info.Latency,
			MemoryUsage: info.MemoryUsage,
			Processed:   info.Processed,
			Failed:      info.Failed,
		})
	}
	return out, nil
}

// DeadLetter is one archived task, i.e. one entry in the dead-letter view.
type DeadLetter struct {
	TaskID     string
	Queue      string
	Type       string
	Payload    []byte
	Retries    int
	MaxRetries int
	LastError  string
	FailedAt   time.Time
}

// DeadLetters returns a page of archived tasks for a queue, newest failure
// first. Page numbers below one become one and page sizes are clamped to
// [1, 100]; a non-positive size uses asynq's default of 30.
func (i *Inspector) DeadLetters(ctx context.Context, queueName string, page, pageSize int) ([]DeadLetter, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if queueName == "" {
		return nil, errors.New("queue: queue name is required")
	}
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = defaultDeadLetterPageSize
	}
	if pageSize > maxDeadLetterPageSize {
		pageSize = maxDeadLetterPageSize
	}

	infos, err := i.inner.ListArchivedTasks(queueName,
		asynq.Page(page),
		asynq.PageSize(pageSize),
	)
	if err != nil {
		return nil, fmt.Errorf("list archived tasks in %q: %w", queueName, err)
	}

	out := make([]DeadLetter, 0, len(infos))
	for _, info := range infos {
		out = append(out, DeadLetter{
			TaskID:     info.ID,
			Queue:      info.Queue,
			Type:       info.Type,
			Payload:    info.Payload,
			Retries:    info.Retried,
			MaxRetries: info.MaxRetry,
			LastError:  info.LastErr,
			FailedAt:   info.LastFailedAt,
		})
	}
	return out, nil
}

// RetryDeadLetter moves an archived task back to pending.
func (i *Inspector) RetryDeadLetter(ctx context.Context, queueName, taskID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if queueName == "" || taskID == "" {
		return errors.New("queue: queue name and task id are required")
	}
	if err := i.inner.RunTask(queueName, taskID); err != nil {
		return fmt.Errorf("retry task %s in %q: %w", taskID, queueName, err)
	}
	return nil
}

// DeleteDeadLetter removes an archived task permanently.
func (i *Inspector) DeleteDeadLetter(ctx context.Context, queueName, taskID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if queueName == "" || taskID == "" {
		return errors.New("queue: queue name and task id are required")
	}
	if err := i.inner.DeleteTask(queueName, taskID); err != nil {
		return fmt.Errorf("delete task %s in %q: %w", taskID, queueName, err)
	}
	return nil
}
