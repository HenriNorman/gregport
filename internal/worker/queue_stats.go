package worker

import (
	"context"
	"log/slog"
	"time"

	"github.com/HenriNorman/gregport/internal/queue"
)

// QueueStatsSource is the subset of queue.Inspector the stats logger needs.
type QueueStatsSource interface {
	Stats(ctx context.Context, queues []string) ([]queue.QueueStats, error)
}

// RunQueueStats logs a queue snapshot immediately and then on every tick until
// ctx is cancelled. A non-positive interval disables the logger.
//
// Archived tasks are logged at warning level because they are the dead-letter
// set: they exhausted their retries and need an operator decision (retry with
// Asynqmon or the queue inspector, or delete them).
func RunQueueStats(ctx context.Context, src QueueStatsSource, logger *slog.Logger, interval time.Duration) {
	if src == nil || interval <= 0 {
		return
	}
	if logger == nil {
		logger = slog.Default()
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		logQueueSnapshot(ctx, src, logger)

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func logQueueSnapshot(ctx context.Context, src QueueStatsSource, logger *slog.Logger) {
	stats, err := src.Stats(ctx, nil)
	if err != nil {
		if ctx.Err() == nil {
			logger.Error("queue statistics failed", "error", err)
		}
		return
	}

	for _, s := range stats {
		attrs := []any{
			"queue", s.Name,
			"size", s.Size,
			"pending", s.Pending,
			"active", s.Active,
			"scheduled", s.Scheduled,
			"retry", s.Retry,
			"archived", s.Archived,
			"completed", s.Completed,
			"paused", s.Paused,
			"latency", s.Latency.String(),
			"memory_bytes", s.MemoryUsage,
			"processed", s.Processed,
			"failed", s.Failed,
		}
		if s.Archived > 0 {
			logger.Warn("queue statistics", attrs...)
			continue
		}
		logger.Info("queue statistics", attrs...)
	}
}
