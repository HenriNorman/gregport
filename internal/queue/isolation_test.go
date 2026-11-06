package queue

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/hibiken/asynq"
)

const isolationTaskCPU = "test:isolation_cpu"
const isolationTaskIO = "test:isolation_io"

// startServerPair starts the production I/O and CPU servers against an
// in-process Redis. Cleanup shuts the servers down before miniredis closes.
func startServerPair(
	t testing.TB,
	ioConcurrency, cpuConcurrency int,
	ioMux, cpuMux *asynq.ServeMux,
) (Config, *Client, *asynq.Server, *asynq.Server) {
	t.Helper()

	mr := miniredis.RunT(t)
	cfg := Config{
		Redis:           RedisOptions{Addr: mr.Addr()},
		IOConcurrency:   ioConcurrency,
		CPUConcurrency:  cpuConcurrency,
		ShutdownTimeout: 5 * time.Second,
	}

	ioServer := NewIOServer(cfg)
	cpuServer := NewCPUServer(cfg)
	if err := ioServer.Start(ioMux); err != nil {
		t.Fatalf("start io server: %v", err)
	}
	t.Cleanup(ioServer.Shutdown)
	if err := cpuServer.Start(cpuMux); err != nil {
		t.Fatalf("start cpu server: %v", err)
	}
	t.Cleanup(cpuServer.Shutdown)

	client := NewClient(cfg)
	t.Cleanup(func() { _ = client.Close() })

	return cfg, client, ioServer, cpuServer
}

func enqueueIsolationJob(t testing.TB, client *Client, id, taskType, queueName, payload string) {
	t.Helper()
	if err := client.EnqueueJob(context.Background(), Job{
		ID:       id,
		Type:     taskType,
		Queue:    queueName,
		Payload:  []byte(payload),
		MaxRetry: 1,
	}); err != nil {
		t.Fatalf("enqueue %s on %s: %v", id, queueName, err)
	}
}

func waitForSignal(t testing.TB, ch <-chan string, want string, timeout time.Duration) {
	t.Helper()
	select {
	case got := <-ch:
		if got != want {
			t.Fatalf("started task %q, want %q", got, want)
		}
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for task %q to start", want)
	}
}

// TestCPUSaturationDoesNotStarveIOQueue fills every CPU worker with a blocking
// task, then proves that I/O tasks still run on the separate I/O server. This
// is the behaviour that makes the two-server split worth its extra connection:
// a thumbnail or export burst cannot stop email delivery or breach checks.
func TestCPUSaturationDoesNotStarveIOQueue(t *testing.T) {
	started := make(chan string, 2)
	var running atomic.Int32

	release := make(chan struct{})
	var releaseOnce sync.Once
	openRelease := func() { releaseOnce.Do(func() { close(release) }) }

	ioMux := asynq.NewServeMux()
	ioDone := make(chan struct{}, 8)
	ioMux.HandleFunc(isolationTaskIO, func(context.Context, *asynq.Task) error {
		ioDone <- struct{}{}
		return nil
	})

	cpuMux := asynq.NewServeMux()
	cpuMux.HandleFunc(isolationTaskCPU, func(_ context.Context, task *asynq.Task) error {
		running.Add(1)
		started <- string(task.Payload())
		<-release
		running.Add(-1)
		return nil
	})

	_, client, _, _ := startServerPair(t, 1, 2, ioMux, cpuMux)
	// Registered after the servers so it runs before their shutdown, which
	// would otherwise wait forever for the blocked CPU handlers.
	t.Cleanup(openRelease)

	const cpuTasks = 2
	for i := 0; i < cpuTasks; i++ {
		enqueueIsolationJob(t, client, "cpu-busy-"+string(rune('a'+i)), isolationTaskCPU, QueueCPU, "cpu")
	}
	for i := 0; i < cpuTasks; i++ {
		waitForSignal(t, started, "cpu", 10*time.Second)
	}
	if got := running.Load(); got != cpuTasks {
		t.Fatalf("CPU workers busy: %d, want %d", got, cpuTasks)
	}

	const ioTasks = 5
	for i := 0; i < ioTasks; i++ {
		enqueueIsolationJob(t, client, "io-"+string(rune('a'+i)), isolationTaskIO, QueueCritical, "io")
	}
	for i := 0; i < ioTasks; i++ {
		select {
		case <-ioDone:
		case <-time.After(10 * time.Second):
			t.Fatalf("I/O task %d did not run while the CPU pool was saturated", i+1)
		}
	}
	if got := running.Load(); got != cpuTasks {
		t.Fatalf("CPU tasks unexpectedly finished before release: %d still running", got)
	}

	openRelease()
}

// TestCPUServerEnforcesItsOwnConcurrency checks that the CPU server honours
// WORKER_CPU_CONCURRENCY instead of draining the queue all at once. A burst of
// CPU work therefore never spawns more than the configured number of
// compute-bound handlers.
func TestCPUServerEnforcesItsOwnConcurrency(t *testing.T) {
	started := make(chan string, 2)
	releaseFirst := make(chan struct{})
	var releaseOnce sync.Once
	openRelease := func() { releaseOnce.Do(func() { close(releaseFirst) }) }

	ioMux := asynq.NewServeMux()
	cpuMux := asynq.NewServeMux()
	cpuMux.HandleFunc(isolationTaskCPU, func(_ context.Context, task *asynq.Task) error {
		id := string(task.Payload())
		started <- id
		if id == "first" {
			<-releaseFirst
		}
		return nil
	})

	_, client, _, _ := startServerPair(t, 1, 1, ioMux, cpuMux)
	t.Cleanup(openRelease)

	enqueueIsolationJob(t, client, "cpu-first", isolationTaskCPU, QueueCPU, "first")
	waitForSignal(t, started, "first", 10*time.Second)

	enqueueIsolationJob(t, client, "cpu-second", isolationTaskCPU, QueueCPU, "second")

	select {
	case got := <-started:
		t.Fatalf("second CPU task %q started while the only CPU worker was busy", got)
	case <-time.After(300 * time.Millisecond):
	}

	openRelease()
	waitForSignal(t, started, "second", 10*time.Second)
}
