package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HenriNorman/gregport/internal/queue"
)

type fakeStatsSource struct {
	mu     sync.Mutex
	calls  int
	stats  []queue.QueueStats
	err    error
	callCh chan struct{}
}

func (f *fakeStatsSource) Stats(context.Context, []string) ([]queue.QueueStats, error) {
	f.mu.Lock()
	f.calls++
	stats, err := f.stats, f.err
	f.mu.Unlock()

	if f.callCh != nil {
		select {
		case f.callCh <- struct{}{}:
		default:
		}
	}
	return stats, err
}

func (f *fakeStatsSource) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func TestRunQueueStatsLogsSnapshots(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	src := &fakeStatsSource{
		stats: []queue.QueueStats{
			{Name: queue.QueueCritical, Size: 3, Pending: 2, Active: 1, Archived: 0},
			{Name: queue.QueueLow, Size: 1, Archived: 1, Retry: 2, Paused: true},
		},
		callCh: make(chan struct{}, 8),
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		RunQueueStats(ctx, src, logger, 5*time.Millisecond)
		close(done)
	}()

	waitForStatsCall(t, src.callCh, "initial snapshot")
	waitForStatsCall(t, src.callCh, "ticked snapshot")
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunQueueStats did not stop after cancellation")
	}

	logs := decodeLogLines(t, buf.Bytes())
	if len(logs) < 4 {
		t.Fatalf("expected at least 4 queue log lines, got %d: %s", len(logs), buf.String())
	}

	var critical, low map[string]any
	for _, entry := range logs {
		if entry["msg"] != "queue statistics" {
			continue
		}
		switch entry["queue"] {
		case queue.QueueCritical:
			critical = entry
		case queue.QueueLow:
			low = entry
		}
	}
	if critical == nil || low == nil {
		t.Fatalf("missing queue logs: %s", buf.String())
	}
	if critical["level"] != "INFO" || critical["pending"] != float64(2) || critical["active"] != float64(1) {
		t.Fatalf("critical log: %v", critical)
	}
	if low["level"] != "WARN" || low["archived"] != float64(1) || low["paused"] != true {
		t.Fatalf("low log: %v", low)
	}
	if !strings.Contains(buf.String(), `"latency":"0s"`) {
		t.Fatalf("latency not logged: %s", buf.String())
	}
}

func TestRunQueueStatsLogsErrorsAndKeepsRunning(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	src := &fakeStatsSource{
		err:    errors.New("redis unavailable"),
		callCh: make(chan struct{}, 8),
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		RunQueueStats(ctx, src, logger, 5*time.Millisecond)
		close(done)
	}()

	waitForStatsCall(t, src.callCh, "first failed snapshot")
	waitForStatsCall(t, src.callCh, "retry after failure")
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunQueueStats did not stop after cancellation")
	}

	if !strings.Contains(buf.String(), "queue statistics failed") ||
		!strings.Contains(buf.String(), "redis unavailable") {
		t.Fatalf("error was not logged: %s", buf.String())
	}
}

func TestRunQueueStatsDisabledInterval(t *testing.T) {
	src := &fakeStatsSource{}

	done := make(chan struct{})
	go func() {
		RunQueueStats(context.Background(), src, statsDiscardLogger(), 0)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("zero interval should disable the logger")
	}
	if src.callCount() != 0 {
		t.Fatalf("disabled logger read queue stats %d times", src.callCount())
	}
}

func TestRunQueueStatsNilSource(t *testing.T) {
	RunQueueStats(context.Background(), nil, statsDiscardLogger(), time.Millisecond)
}

func TestRunQueueStatsStopsWithoutLoggingAfterCancel(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	src := &fakeStatsSource{callCh: make(chan struct{}, 8)}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		RunQueueStats(ctx, src, logger, time.Hour)
		close(done)
	}()

	waitForStatsCall(t, src.callCh, "initial snapshot")
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("RunQueueStats did not stop")
	}
}

func waitForStatsCall(t *testing.T, ch <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
	}
}

func decodeLogLines(t *testing.T, raw []byte) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal(line, &entry); err != nil {
			t.Fatalf("decode log line %q: %v", line, err)
		}
		out = append(out, entry)
	}
	return out
}

func statsDiscardLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}
