package search

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"

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
