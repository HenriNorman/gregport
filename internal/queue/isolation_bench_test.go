package queue

import (
	"context"
	"fmt"
	"testing"

	"github.com/hibiken/asynq"
)

// BenchmarkQueueRoundTrip measures enqueue-to-completion latency for a
// no-op task on each queue. It is the control measurement for worker sizing:
// it shows how much of a task's latency is queue and Redis overhead rather
// than handler work. Run it with:
//
//	go test ./internal/queue -run '^$' -bench BenchmarkQueueRoundTrip -benchtime=200x
func BenchmarkQueueRoundTrip(b *testing.B) {
	cases := []struct {
		name     string
		taskType string
		queue    string
	}{
		{name: "io", taskType: isolationTaskIO, queue: QueueCritical},
		{name: "cpu", taskType: isolationTaskCPU, queue: QueueCPU},
	}

	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			done := make(chan struct{}, 1)
			ioMux := asynq.NewServeMux()
			cpuMux := asynq.NewServeMux()

			handler := func(context.Context, *asynq.Task) error {
				done <- struct{}{}
				return nil
			}
			if tc.queue == QueueCPU {
				cpuMux.HandleFunc(tc.taskType, handler)
			} else {
				ioMux.HandleFunc(tc.taskType, handler)
			}

			_, client, _, _ := startServerPair(b, 1, 1, ioMux, cpuMux)

			// Warm up connections and the asynq polling loop before timing.
			enqueueIsolationJob(b, client, "warmup", tc.taskType, tc.queue, "warmup")
			<-done

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				id := fmt.Sprintf("%s-%d", tc.name, i)
				enqueueIsolationJob(b, client, id, tc.taskType, tc.queue, "bench")
				<-done
			}
		})
	}
}
