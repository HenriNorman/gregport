// Package queue configures the Redis-backed work queue. The queue is used for
// downstream consumers of ticket events; the durable source of truth is the
// PostgreSQL outbox in ticket_events, so Redis loss cannot lose work.
package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/hibiken/asynq"
)

// Queue names. Priority weights are defined in IOQueues.
const (
	QueueCritical = "critical"
	QueueDefault  = "default"
	QueueLow      = "low"
	QueueCPU      = "cpu"
)

// Task types.
const (
	TaskEmailSend       = "email:send"
	TaskSLABreachCheck  = "sla:breach_check"
	TaskAutoClose       = "ticket:auto_close"
	TaskHousekeeping    = "housekeeping:run"
	TaskNotify          = "notify:send"
	TaskWebhookDispatch = "webhook:dispatch"
	TaskWebhookDeliver  = "webhook:deliver"
	TaskNoop            = "system:noop"
)

// IOQueues is the weighted priority map for I/O-bound work. Weights are
// relative: with all queues busy, critical/default/low are served roughly
// 60/30/10 percent of the time.
func IOQueues() map[string]int {
	return map[string]int{
		QueueCritical: 6,
		QueueDefault:  3,
		QueueLow:      1,
	}
}

// CPUQueues isolates CPU-bound work on its own server so it cannot occupy the
// I/O worker pool.
func CPUQueues() map[string]int {
	return map[string]int{QueueCPU: 1}
}

// RedisOptions configures the asynq Redis connection.
type RedisOptions struct {
	Addr         string
	Password     string
	DB           int
	DialTimeout  time.Duration
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
}

// Config configures clients and servers.
type Config struct {
	Redis           RedisOptions
	IOConcurrency   int
	CPUConcurrency  int
	ShutdownTimeout time.Duration
	Logger          *slog.Logger
}

func redisOpt(o RedisOptions) asynq.RedisClientOpt {
	return asynq.RedisClientOpt{
		Addr:         o.Addr,
		Password:     o.Password,
		DB:           o.DB,
		DialTimeout:  o.DialTimeout,
		ReadTimeout:  o.ReadTimeout,
		WriteTimeout: o.WriteTimeout,
	}
}

// Client is the enqueue side of the queue. It satisfies the dispatcher's
// small interface so tests can substitute a recorder.
type Client struct {
	inner *asynq.Client
}

// NewClient creates a queue client. It does not connect until the first
// Enqueue call.
func NewClient(cfg Config) *Client {
	return &Client{inner: asynq.NewClient(redisOpt(cfg.Redis))}
}

// Enqueue adds task to its queue. Options control queue, id, retries and
// delays.
func (c *Client) Enqueue(ctx context.Context, task *asynq.Task, opts ...asynq.Option) error {
	_, err := c.inner.EnqueueContext(ctx, task, opts...)
	return err
}

// Job is a queue task with the metadata the outbox needs to make enqueueing
// deterministic and testable.
type Job struct {
	ID       string
	Type     string
	Queue    string
	Payload  []byte
	MaxRetry int
}

// Validate rejects incomplete jobs before they reach Redis.
func (j Job) Validate() error {
	if j.ID == "" {
		return fmt.Errorf("queue: job id is required")
	}
	if j.Type == "" {
		return fmt.Errorf("queue: job type is required")
	}
	if j.Queue == "" {
		return fmt.Errorf("queue: job queue is required")
	}
	if j.MaxRetry < 1 {
		return fmt.Errorf("queue: job max retry must be positive")
	}
	return nil
}

// EnqueueJob enqueues a Job with its deterministic task ID. A duplicate ID
// means another dispatcher already enqueued the same logical job, which is
// success for an at-least-once pipeline.
func (c *Client) EnqueueJob(ctx context.Context, job Job) error {
	if err := job.Validate(); err != nil {
		return err
	}

	opts := []asynq.Option{
		asynq.Queue(job.Queue),
		asynq.TaskID(job.ID),
		asynq.MaxRetry(job.MaxRetry),
	}
	_, err := c.inner.EnqueueContext(ctx, asynq.NewTask(job.Type, job.Payload), opts...)
	if errors.Is(err, asynq.ErrTaskIDConflict) {
		return nil
	}
	return err
}

// Ping reports Redis reachability. It lets the worker readiness probe fail
// when the queue backend is unavailable.
func (c *Client) Ping(context.Context) error {
	return c.inner.Ping()
}

// Close releases the Redis connections.
func (c *Client) Close() error {
	return c.inner.Close()
}

// NewIOServer builds the server for I/O-bound queues. Concurrency defaults to
// a value suitable for jobs that mostly wait on network I/O.
func NewIOServer(cfg Config) *asynq.Server {
	return asynq.NewServer(redisOpt(cfg.Redis), asynq.Config{
		Concurrency:     cfg.IOConcurrency,
		Queues:          IOQueues(),
		RetryDelayFunc:  RetryDelayForTask,
		ErrorHandler:    &errorHandler{logger: loggerOrDiscard(cfg.Logger)},
		Logger:          slogAdapter{logger: loggerOrDiscard(cfg.Logger)},
		LogLevel:        asynq.InfoLevel,
		ShutdownTimeout: cfg.ShutdownTimeout,
	})
}

// NewCPUServer builds the server for CPU-bound work. Its concurrency should
// be close to the number of usable cores.
func NewCPUServer(cfg Config) *asynq.Server {
	return asynq.NewServer(redisOpt(cfg.Redis), asynq.Config{
		Concurrency:     cfg.CPUConcurrency,
		Queues:          CPUQueues(),
		RetryDelayFunc:  RetryDelayForTask,
		ErrorHandler:    &errorHandler{logger: loggerOrDiscard(cfg.Logger)},
		Logger:          slogAdapter{logger: loggerOrDiscard(cfg.Logger)},
		LogLevel:        asynq.InfoLevel,
		ShutdownTimeout: cfg.ShutdownTimeout,
	})
}

// NewTask marshals payload into an asynq task.
func NewTask(taskType string, payload any) (*asynq.Task, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal %s payload: %w", taskType, err)
	}
	return asynq.NewTask(taskType, raw), nil
}

// Permanent wraps err so asynq archives the task immediately instead of
// scheduling retries. Use it for failures that cannot succeed on a retry.
func Permanent(err error) error {
	return fmt.Errorf("%w: %w", asynq.SkipRetry, err)
}

func loggerOrDiscard(l *slog.Logger) *slog.Logger {
	if l != nil {
		return l
	}
	return slog.New(slog.NewJSONHandler(discardWriter{}, nil))
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// slogAdapter sends asynq's own logs through slog so both binaries emit one
// JSON stream.
type slogAdapter struct{ logger *slog.Logger }

func (a slogAdapter) Debug(args ...interface{}) { a.logger.Debug(fmt.Sprint(args...)) }
func (a slogAdapter) Info(args ...interface{})  { a.logger.Info(fmt.Sprint(args...)) }
func (a slogAdapter) Warn(args ...interface{})  { a.logger.Warn(fmt.Sprint(args...)) }
func (a slogAdapter) Error(args ...interface{}) { a.logger.Error(fmt.Sprint(args...)) }
func (a slogAdapter) Fatal(args ...interface{}) { a.logger.Error(fmt.Sprint(args...)) }

// errorHandler logs retries and exhausted tasks. The archived set is the
// dead-letter view; exhausted tasks remain inspectable in Asynqmon.
type errorHandler struct{ logger *slog.Logger }

func (h *errorHandler) HandleError(ctx context.Context, task *asynq.Task, err error) {
	retried, _ := asynq.GetRetryCount(ctx)
	maxRetry, _ := asynq.GetMaxRetry(ctx)

	attrs := []any{
		"task_type", task.Type(),
		"retry", retried,
		"max_retry", maxRetry,
		"error", err,
	}
	if retried >= maxRetry {
		h.logger.Error("task exhausted retries; archived", attrs...)
		return
	}
	h.logger.Warn("task failed; retry scheduled", attrs...)
}
