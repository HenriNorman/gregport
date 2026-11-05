package queue

import (
	"context"
	"errors"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/hibiken/asynq"
)

func newInspectorFixture(t *testing.T) (Config, *Client, *Inspector) {
	t.Helper()

	mr := miniredis.RunT(t)
	cfg := Config{Redis: RedisOptions{Addr: mr.Addr()}}

	client := NewClient(cfg)
	t.Cleanup(func() { _ = client.Close() })

	inspector := NewInspector(cfg)
	t.Cleanup(func() { _ = inspector.Close() })

	return cfg, client, inspector
}

func enqueueTestJob(t *testing.T, client *Client, id, queueName string, maxRetry int) {
	t.Helper()
	err := client.EnqueueJob(context.Background(), Job{
		ID:       id,
		Type:     TaskNoop,
		Queue:    queueName,
		Payload:  []byte(`{"hello":"world"}`),
		MaxRetry: maxRetry,
	})
	if err != nil {
		t.Fatalf("enqueue %s: %v", id, err)
	}
}

func archiveTestJob(t *testing.T, cfg Config, queueName, id string) {
	t.Helper()
	inner := asynq.NewInspector(asynq.RedisClientOpt{Addr: cfg.Redis.Addr})
	defer inner.Close()
	if err := inner.ArchiveTask(queueName, id); err != nil {
		t.Fatalf("archive %s: %v", id, err)
	}
}

func TestInspectorStats(t *testing.T) {
	_, client, inspector := newInspectorFixture(t)

	enqueueTestJob(t, client, "stats-1", QueueCritical, 3)
	enqueueTestJob(t, client, "stats-2", QueueCritical, 3)
	enqueueTestJob(t, client, "stats-3", QueueDefault, 3)

	stats, err := inspector.Stats(context.Background(), []string{QueueCritical})
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if len(stats) != 1 {
		t.Fatalf("stats: %+v", stats)
	}
	got := stats[0]
	if got.Name != QueueCritical || got.Pending != 2 || got.Size != 2 {
		t.Fatalf("critical stats: %+v", got)
	}
	if got.Archived != 0 || got.Active != 0 || got.Paused {
		t.Fatalf("unexpected state: %+v", got)
	}

	all, err := inspector.Stats(context.Background(), nil)
	if err != nil {
		t.Fatalf("all stats: %v", err)
	}
	byName := map[string]QueueStats{}
	for _, s := range all {
		byName[s.Name] = s
	}
	if byName[QueueCritical].Pending != 2 || byName[QueueDefault].Pending != 1 {
		t.Fatalf("all stats: %+v", all)
	}

	queues, err := inspector.Queues(context.Background())
	if err != nil {
		t.Fatalf("queues: %v", err)
	}
	if len(queues) != 2 {
		t.Fatalf("queues: %v", queues)
	}
}

func TestInspectorDeadLetterLifecycle(t *testing.T) {
	cfg, client, inspector := newInspectorFixture(t)
	enqueueTestJob(t, client, "archive-1", QueueCritical, 3)
	archiveTestJob(t, cfg, QueueCritical, "archive-1")

	dead, err := inspector.DeadLetters(context.Background(), QueueCritical, 1, 10)
	if err != nil {
		t.Fatalf("dead letters: %v", err)
	}
	if len(dead) != 1 {
		t.Fatalf("dead letters: %+v", dead)
	}
	got := dead[0]
	if got.TaskID != "archive-1" || got.Queue != QueueCritical || got.Type != TaskNoop {
		t.Fatalf("dead letter: %+v", got)
	}
	if got.MaxRetries != 3 || string(got.Payload) != `{"hello":"world"}` {
		t.Fatalf("dead letter metadata: %+v", got)
	}

	stats, err := inspector.Stats(context.Background(), []string{QueueCritical})
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats[0].Archived != 1 || stats[0].Pending != 0 {
		t.Fatalf("archived stats: %+v", stats[0])
	}

	if err := inspector.RetryDeadLetter(context.Background(), QueueCritical, "archive-1"); err != nil {
		t.Fatalf("retry: %v", err)
	}
	dead, err = inspector.DeadLetters(context.Background(), QueueCritical, 1, 10)
	if err != nil {
		t.Fatalf("dead letters after retry: %v", err)
	}
	if len(dead) != 0 {
		t.Fatalf("task still archived: %+v", dead)
	}

	// Archive again and delete permanently.
	archiveTestJob(t, cfg, QueueCritical, "archive-1")
	if err := inspector.DeleteDeadLetter(context.Background(), QueueCritical, "archive-1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	dead, err = inspector.DeadLetters(context.Background(), QueueCritical, 1, 10)
	if err != nil {
		t.Fatalf("dead letters after delete: %v", err)
	}
	if len(dead) != 0 {
		t.Fatalf("task still present after delete: %+v", dead)
	}

	stats, err = inspector.Stats(context.Background(), []string{QueueCritical})
	if err != nil {
		t.Fatalf("stats after delete: %v", err)
	}
	if stats[0].Archived != 0 || stats[0].Size != 0 {
		t.Fatalf("queue not empty after delete: %+v", stats[0])
	}
}

func TestInspectorDeadLetterPaging(t *testing.T) {
	cfg, client, inspector := newInspectorFixture(t)

	for _, id := range []string{"page-1", "page-2", "page-3", "page-4", "page-5"} {
		enqueueTestJob(t, client, id, QueueCritical, 3)
		archiveTestJob(t, cfg, QueueCritical, id)
	}

	page1, err := inspector.DeadLetters(context.Background(), QueueCritical, 1, 2)
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	page2, err := inspector.DeadLetters(context.Background(), QueueCritical, 2, 2)
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}
	page3, err := inspector.DeadLetters(context.Background(), QueueCritical, 3, 2)
	if err != nil {
		t.Fatalf("page 3: %v", err)
	}
	if len(page1) != 2 || len(page2) != 2 || len(page3) != 1 {
		t.Fatalf("page sizes: %d %d %d", len(page1), len(page2), len(page3))
	}

	seen := map[string]bool{}
	for _, page := range [][]DeadLetter{page1, page2, page3} {
		for _, dl := range page {
			if seen[dl.TaskID] {
				t.Fatalf("task repeated across pages: %s", dl.TaskID)
			}
			seen[dl.TaskID] = true
		}
	}
	if len(seen) != 5 {
		t.Fatalf("tasks across pages: %v", seen)
	}

	// Page zero is normalized to the first page and an oversized page size is
	// clamped without breaking the listing.
	first, err := inspector.DeadLetters(context.Background(), QueueCritical, 0, 0)
	if err != nil {
		t.Fatalf("page zero: %v", err)
	}
	if len(first) != 5 {
		t.Fatalf("page zero returned %d tasks", len(first))
	}
	clamped, err := inspector.DeadLetters(context.Background(), QueueCritical, 1, 1000)
	if err != nil {
		t.Fatalf("oversized page: %v", err)
	}
	if len(clamped) != 5 {
		t.Fatalf("oversized page returned %d tasks", len(clamped))
	}
}

func TestInspectorValidation(t *testing.T) {
	_, _, inspector := newInspectorFixture(t)
	ctx := context.Background()

	if _, err := inspector.DeadLetters(ctx, "", 1, 10); err == nil {
		t.Fatal("expected an error for an empty queue name")
	}
	if err := inspector.RetryDeadLetter(ctx, "", "id"); err == nil {
		t.Fatal("expected an error for an empty queue name")
	}
	if err := inspector.RetryDeadLetter(ctx, QueueCritical, ""); err == nil {
		t.Fatal("expected an error for an empty task id")
	}
	if err := inspector.DeleteDeadLetter(ctx, "", "id"); err == nil {
		t.Fatal("expected an error for an empty queue name")
	}
	if err := inspector.DeleteDeadLetter(ctx, QueueCritical, ""); err == nil {
		t.Fatal("expected an error for an empty task id")
	}
}

func TestInspectorRespectsCancelledContext(t *testing.T) {
	_, _, inspector := newInspectorFixture(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := inspector.Queues(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Queues error: %v", err)
	}
	if _, err := inspector.Stats(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("Stats error: %v", err)
	}
	if _, err := inspector.DeadLetters(ctx, QueueCritical, 1, 10); !errors.Is(err, context.Canceled) {
		t.Fatalf("DeadLetters error: %v", err)
	}
	if err := inspector.RetryDeadLetter(ctx, QueueCritical, "id"); !errors.Is(err, context.Canceled) {
		t.Fatalf("RetryDeadLetter error: %v", err)
	}
	if err := inspector.DeleteDeadLetter(ctx, QueueCritical, "id"); !errors.Is(err, context.Canceled) {
		t.Fatalf("DeleteDeadLetter error: %v", err)
	}
}
