package metrics

import (
	"context"
	"time"

	"github.com/HenriNorman/gregport/internal/queue"
)

// QueueInspector is the subset of queue.Inspector needed for metrics.
type QueueInspector interface {
	Stats(ctx context.Context, queues []string) ([]queue.QueueStats, error)
}

// RunQueueMetrics samples queue depth until ctx ends. Prometheus scrapes the
// gauges, so this poll interval bounds freshness rather than scrape cost.
func RunQueueMetrics(ctx context.Context, inspector QueueInspector, p *Prometheus, interval time.Duration) {
	if inspector == nil || p == nil || interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	sample := func() {
		stats, err := inspector.Stats(ctx, nil)
		if err != nil {
			return
		}
		p.QueueDepth.Reset()
		p.QueueArchived.Reset()
		for _, s := range stats {
			p.QueueDepth.WithLabelValues(s.Name, "pending").Set(float64(s.Pending))
			p.QueueDepth.WithLabelValues(s.Name, "active").Set(float64(s.Active))
			p.QueueDepth.WithLabelValues(s.Name, "scheduled").Set(float64(s.Scheduled))
			p.QueueDepth.WithLabelValues(s.Name, "retry").Set(float64(s.Retry))
			p.QueueArchived.WithLabelValues(s.Name).Set(float64(s.Archived))
		}
	}

	sample()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sample()
		}
	}
}
