package queue

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/hibiken/asynq"
)

func TestQueueWeights(t *testing.T) {
	io := IOQueues()
	if io[QueueCritical] != 6 || io[QueueDefault] != 3 || io[QueueLow] != 1 {
		t.Fatalf("io queue weights: %+v", io)
	}
	cpu := CPUQueues()
	if cpu[QueueCPU] != 1 || len(cpu) != 1 {
		t.Fatalf("cpu queue weights: %+v", cpu)
	}
}

func TestNewTaskMarshalsPayload(t *testing.T) {
	task, err := NewTask(TaskEmailSend, map[string]any{"to": "a@b.c"})
	if err != nil {
		t.Fatalf("new task: %v", err)
	}
	if task.Type() != TaskEmailSend {
		t.Fatalf("type: %q", task.Type())
	}
	var got map[string]string
	if err := json.Unmarshal(task.Payload(), &got); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if got["to"] != "a@b.c" {
		t.Fatalf("payload: %v", got)
	}
}

func TestNewTaskRejectsUnmarshalablePayload(t *testing.T) {
	if _, err := NewTask(TaskNoop, make(chan int)); err == nil {
		t.Fatal("expected an error for an unmarshalable payload")
	}
}

func TestPermanentSkipsRetry(t *testing.T) {
	err := Permanent(errors.New("invalid recipient"))
	if !errors.Is(err, asynq.SkipRetry) {
		t.Fatalf("Permanent must wrap SkipRetry: %v", err)
	}
	if !strings.Contains(err.Error(), "invalid recipient") {
		t.Fatalf("Permanent must keep the underlying message: %v", err)
	}
}

func TestNewServersConstructed(t *testing.T) {
	cfg := Config{
		Redis:           RedisOptions{Addr: "127.0.0.1:6379"},
		IOConcurrency:   24,
		CPUConcurrency:  4,
		ShutdownTimeout: 30 * time.Second,
	}
	if NewIOServer(cfg) == nil || NewCPUServer(cfg) == nil {
		t.Fatal("servers must not be nil")
	}
}

func TestErrorHandlerLogsExhaustedTask(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	h := &errorHandler{logger: logger}

	h.HandleError(context.Background(), asynq.NewTask(TaskEmailSend, []byte(`{}`)), errors.New("smtp down"))

	if !bytes.Contains(buf.Bytes(), []byte("archived")) {
		t.Fatalf("expected archived log, got %s", buf.String())
	}
	if !bytes.Contains(buf.Bytes(), []byte(TaskEmailSend)) {
		t.Fatalf("log must include task type: %s", buf.String())
	}
}
