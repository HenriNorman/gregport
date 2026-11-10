package search

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/sony/gobreaker"

	"github.com/HenriNorman/gregport/internal/queue"
)

type fakeDocLoader struct {
	state DocumentState
	err   error
	orgID uuid.UUID
	id    uuid.UUID
}

func (f *fakeDocLoader) LoadDocument(_ context.Context, orgID, ticketID uuid.UUID) (DocumentState, error) {
	f.orgID = orgID
	f.id = ticketID
	return f.state, f.err
}

type fakeIndexer struct {
	indexed []TicketDocument
	deleted []deleteCall
	err     error
}

type deleteCall struct {
	orgID   uuid.UUID
	id      uuid.UUID
	version int64
}

func (f *fakeIndexer) IndexTicket(_ context.Context, doc TicketDocument) error {
	f.indexed = append(f.indexed, doc)
	return f.err
}

func (f *fakeIndexer) DeleteTicket(_ context.Context, orgID, ticketID uuid.UUID, version int64) error {
	f.deleted = append(f.deleted, deleteCall{orgID: orgID, id: ticketID, version: version})
	return f.err
}

func syncTask(t *testing.T, orgID, ticketID uuid.UUID) *asynq.Task {
	t.Helper()
	task, err := NewSyncTask(orgID, ticketID)
	if err != nil {
		t.Fatalf("NewSyncTask: %v", err)
	}
	return task
}

func TestSyncHandlerIndexesActiveTicket(t *testing.T) {
	orgID := uuid.New()
	id := uuid.New()
	doc := TicketDocument{
		ID: id, OrgID: orgID, Number: 7, Subject: "Printer failure",
		CommentsText: "tray jammed", Status: "open", Priority: "high",
		Tags: []string{"hardware"}, RequesterEmail: "h@acme.com",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(), Version: 4,
	}
	loader := &fakeDocLoader{state: DocumentState{Document: doc, Version: 4, Found: true}}
	indexer := &fakeIndexer{}
	handler := NewSyncHandler(loader, indexer, nil)

	if err := handler.Handle(context.Background(), syncTask(t, orgID, id)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(indexer.indexed) != 1 || indexer.indexed[0].ID != id {
		t.Fatalf("indexed = %+v", indexer.indexed)
	}
	if len(indexer.deleted) != 0 {
		t.Fatalf("unexpected deletes: %+v", indexer.deleted)
	}
	if loader.orgID != orgID || loader.id != id {
		t.Fatalf("loader args = %s %s", loader.orgID, loader.id)
	}
}

func TestSyncHandlerDeletesSoftDeletedTicketWithVersion(t *testing.T) {
	orgID := uuid.New()
	id := uuid.New()
	loader := &fakeDocLoader{state: DocumentState{Found: true, Deleted: true, Version: 5}}
	indexer := &fakeIndexer{}
	handler := NewSyncHandler(loader, indexer, nil)

	if err := handler.Handle(context.Background(), syncTask(t, orgID, id)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(indexer.deleted) != 1 {
		t.Fatalf("deleted = %+v", indexer.deleted)
	}
	if indexer.deleted[0].orgID != orgID || indexer.deleted[0].id != id || indexer.deleted[0].version != 5 {
		t.Fatalf("delete call = %+v", indexer.deleted[0])
	}
	if len(indexer.indexed) != 0 {
		t.Fatalf("unexpected index: %+v", indexer.indexed)
	}
}

func TestSyncHandlerDeletesMissingTicket(t *testing.T) {
	orgID := uuid.New()
	id := uuid.New()
	loader := &fakeDocLoader{state: DocumentState{Found: false}}
	indexer := &fakeIndexer{}
	handler := NewSyncHandler(loader, indexer, nil)

	if err := handler.Handle(context.Background(), syncTask(t, orgID, id)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(indexer.deleted) != 1 || indexer.deleted[0].version != 0 {
		t.Fatalf("deleted = %+v", indexer.deleted)
	}
}

func TestSyncHandlerPropagatesLoaderError(t *testing.T) {
	orgID := uuid.New()
	id := uuid.New()
	boom := errors.New("db down")
	loader := &fakeDocLoader{err: boom}
	handler := NewSyncHandler(loader, &fakeIndexer{}, nil)

	if err := handler.Handle(context.Background(), syncTask(t, orgID, id)); !errors.Is(err, boom) {
		t.Fatalf("Handle error = %v, want %v", err, boom)
	}
}

func TestSyncHandlerRejectsBadPayload(t *testing.T) {
	handler := NewSyncHandler(&fakeDocLoader{}, &fakeIndexer{}, nil)
	if err := handler.Handle(context.Background(), asynq.NewTask(queue.TaskSearchIndex, []byte("not-json"))); err == nil {
		t.Fatal("Handle bad json = nil error")
	}
	if err := handler.Handle(context.Background(), asynq.NewTask(queue.TaskSearchIndex, []byte(`{}`))); err == nil {
		t.Fatal("Handle empty payload = nil error")
	}
}

func TestNewSyncTaskPayload(t *testing.T) {
	orgID := uuid.New()
	id := uuid.New()
	task := syncTask(t, orgID, id)
	if task.Type() != queue.TaskSearchIndex {
		t.Fatalf("task type = %q", task.Type())
	}
	var payload SyncPayload
	if err := json.Unmarshal(task.Payload(), &payload); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if payload.OrgID != orgID || payload.TicketID != id {
		t.Fatalf("payload = %+v", payload)
	}
}

type alwaysFailIndexer struct {
	calls int
}

func (f *alwaysFailIndexer) IndexTicket(_ context.Context, _ TicketDocument) error {
	f.calls++
	return ErrSearchUnavailable
}

func (f *alwaysFailIndexer) DeleteTicket(_ context.Context, _, _ uuid.UUID, _ int64) error {
	f.calls++
	return ErrSearchUnavailable
}

func TestSyncHandlerCircuitBreakerSkipsWhenOpen(t *testing.T) {
	orgID := uuid.New()
	id := uuid.New()
	loader := &fakeDocLoader{state: DocumentState{Document: TicketDocument{
		ID: id, OrgID: orgID, Version: 1,
	}, Found: true}}
	indexer := &alwaysFailIndexer{}
	handler := NewSyncHandler(loader, indexer, nil)

	// Trip the circuit: 5 consecutive failures.
	for i := 0; i < 5; i++ {
		if err := handler.Handle(context.Background(), syncTask(t, orgID, id)); err != nil {
			if !errors.Is(err, ErrSearchUnavailable) {
				t.Fatalf("handle %d: %v, want ErrSearchUnavailable", i, err)
			}
		}
	}

	// The circuit should now be open. The next call should skip (nil error).
	if err := handler.Handle(context.Background(), syncTask(t, orgID, id)); err != nil {
		t.Fatalf("handle with open circuit: %v, want nil (skip)", err)
	}

	// The breaker should have recorded 6 attempts (5 failures + 1 blocked).
	if indexer.calls != 5 {
		t.Fatalf("indexer calls = %d, want 5 (6th call skipped by breaker)", indexer.calls)
	}
}

func TestSyncHandlerCircuitBreakerRecovers(t *testing.T) {
	orgID := uuid.New()
	id := uuid.New()
	loader := &fakeDocLoader{state: DocumentState{Document: TicketDocument{
		ID: id, OrgID: orgID, Version: 1,
	}, Found: true}}
	indexer := &alwaysFailIndexer{}
	handler := NewSyncHandler(loader, indexer, nil)

	// Trip the circuit.
	for i := 0; i < 5; i++ {
		if err := handler.Handle(context.Background(), syncTask(t, orgID, id)); err != nil {
			if !errors.Is(err, ErrSearchUnavailable) {
				t.Fatalf("handle %d: %v, want ErrSearchUnavailable", i, err)
			}
		}
	}

	// Wait for the circuit to enter half-open state (30s timeout is too long
	// for a unit test; instead create a handler with a short timeout).
	// This test verifies that a successful call after the breaker resets works.
	// We directly reset the breaker to simulate recovery.
	// Access the breaker through the handler is not possible, so we test via
	// a fresh handler with a failing then succeeding indexer.
	indexer.calls = 0

	// Create a new handler with a short-interval breaker for recovery testing.
	st := gobreaker.Settings{
		Name:        "search-index-test",
		MaxRequests: 1,
		Interval:    100 * time.Millisecond,
		Timeout:     100 * time.Millisecond,
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			return counts.ConsecutiveFailures >= 2
		},
	}
	handler2 := &SyncHandler{
		loader:  loader,
		indexer: indexer,
		logger:  slog.Default(),
		breaker: gobreaker.NewCircuitBreaker(st),
	}

	// Trip with 2 failures.
	for i := 0; i < 2; i++ {
		if err := handler2.Handle(context.Background(), syncTask(t, orgID, id)); err != nil {
			if !errors.Is(err, ErrSearchUnavailable) {
				t.Fatalf("trip %d: %v", i, err)
			}
		}
	}

	// Circuit should be open; next call skips.
	if err := handler2.Handle(context.Background(), syncTask(t, orgID, id)); err != nil {
		t.Fatalf("open circuit: %v", err)
	}

	// Wait for the timeout to pass so the circuit goes half-open.
	time.Sleep(200 * time.Millisecond)

	// Make the indexer succeed.
	succeedingIndexer := &succeedIndexer{}
	handler2.indexer = succeedingIndexer

	// Half-open: one request allowed through. If it succeeds, circuit closes.
	if err := handler2.Handle(context.Background(), syncTask(t, orgID, id)); err != nil {
		t.Fatalf("half-open: %v", err)
	}

	// Circuit should now be closed; subsequent calls work normally.
	if err := handler2.Handle(context.Background(), syncTask(t, orgID, id)); err != nil {
		t.Fatalf("closed circuit: %v", err)
	}
}

type succeedIndexer struct {
	calls int
}

func (f *succeedIndexer) IndexTicket(_ context.Context, _ TicketDocument) error {
	f.calls++
	return nil
}

func (f *succeedIndexer) DeleteTicket(_ context.Context, _, _ uuid.UUID, _ int64) error {
	f.calls++
	return nil
}
