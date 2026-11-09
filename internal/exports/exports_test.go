package exports

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"

	"github.com/HenriNorman/gregport/internal/email"
	"github.com/HenriNorman/gregport/internal/queue"
	"github.com/HenriNorman/gregport/internal/storage"
)

type fakeRepo struct {
	mu        sync.Mutex
	jobs      map[uuid.UUID]Job
	rows      []TicketRow
	rowOrg    uuid.UUID
	generator func(after time.Time, limit int) []TicketRow
	listErr   error
	recEmail  string
	orgName   string
	recipErr  error
	listCalls []uuid.UUID
	listAfter []time.Time
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{jobs: make(map[uuid.UUID]Job)}
}

func (f *fakeRepo) CreateJob(_ context.Context, orgID, requestedBy uuid.UUID, filters []byte) (Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	j := Job{
		ID:          uuid.New(),
		OrgID:       orgID,
		RequestedBy: requestedBy,
		Status:      StatusPending,
		Filters:     append([]byte(nil), filters...),
		CreatedAt:   time.Now().UTC(),
	}
	f.jobs[j.ID] = j
	return j, nil
}

func (f *fakeRepo) GetJob(_ context.Context, orgID, id uuid.UUID) (Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	j, ok := f.jobs[id]
	if !ok || j.OrgID != orgID {
		return Job{}, ErrExportNotFound
	}
	return j, nil
}

func (f *fakeRepo) MarkRunning(_ context.Context, orgID, id uuid.UUID) (Job, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	j, ok := f.jobs[id]
	if !ok || j.OrgID != orgID || j.Status == StatusDone {
		return Job{}, false, nil
	}
	j.Status = StatusRunning
	j.Error = ""
	f.jobs[id] = j
	return j, true, nil
}

func (f *fakeRepo) MarkDone(_ context.Context, orgID, id uuid.UUID, key string, rows int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	j, ok := f.jobs[id]
	if !ok || j.OrgID != orgID {
		return ErrExportNotFound
	}
	now := time.Now().UTC()
	j.Status = StatusDone
	j.StorageKey = key
	j.RowCount = &rows
	j.CompletedAt = &now
	f.jobs[id] = j
	return nil
}

func (f *fakeRepo) MarkFailed(_ context.Context, orgID, id uuid.UUID, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	j, ok := f.jobs[id]
	if !ok || j.OrgID != orgID {
		return ErrExportNotFound
	}
	j.Status = StatusFailed
	j.Error = reason
	f.jobs[id] = j
	return nil
}

func (f *fakeRepo) LoadRecipient(_ context.Context, _, _ uuid.UUID) (string, string, error) {
	if f.recipErr != nil {
		return "", "", f.recipErr
	}
	return f.recEmail, f.orgName, nil
}

func (f *fakeRepo) ListTicketRows(_ context.Context, orgID uuid.UUID, _ Filter, after time.Time, afterID uuid.UUID, limit int) ([]TicketRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listCalls = append(f.listCalls, orgID)
	f.listAfter = append(f.listAfter, after)
	if f.listErr != nil {
		return nil, f.listErr
	}
	if f.generator != nil {
		return f.generator(after, limit), nil
	}
	if f.rowOrg != uuid.Nil && orgID != f.rowOrg {
		return nil, nil
	}

	start := 0
	if !after.IsZero() {
		for i, r := range f.rows {
			if r.CreatedAt.Before(after) || (r.CreatedAt.Equal(after) && r.ID.String() < afterID.String()) {
				start = i
				break
			}
		}
		if start == 0 {
			// Cursor past every row.
			last := f.rows[len(f.rows)-1]
			if !last.CreatedAt.Before(after) {
				return nil, nil
			}
		}
	}
	end := start + limit
	if end > len(f.rows) {
		end = len(f.rows)
	}
	out := make([]TicketRow, end-start)
	copy(out, f.rows[start:end])
	return out, nil
}

type fakeStore struct {
	mu       sync.Mutex
	puts     map[string][]byte
	putTypes map[string]string
	putErr   error
	presign  string
}

func newFakeStore() *fakeStore {
	return &fakeStore{puts: make(map[string][]byte), putTypes: make(map[string]string)}
}

func (f *fakeStore) PresignPut(_ context.Context, key string, _ time.Duration) (string, error) {
	return "https://storage.test/put/" + key, nil
}

func (f *fakeStore) PresignGet(_ context.Context, key string, _ time.Duration, disposition string) (string, error) {
	if f.presign != "" {
		return f.presign, nil
	}
	return "https://storage.test/get/" + key + "?disposition=" + disposition, nil
}

func (f *fakeStore) Head(context.Context, string) (storage.ObjectInfo, error) {
	return storage.ObjectInfo{}, storage.ErrObjectNotFound
}

func (f *fakeStore) GetRange(context.Context, string, int64) ([]byte, error) {
	return nil, storage.ErrObjectNotFound
}

func (f *fakeStore) Open(context.Context, string) (io.ReadCloser, storage.ObjectInfo, error) {
	return nil, storage.ObjectInfo{}, storage.ErrObjectNotFound
}

func (f *fakeStore) Put(_ context.Context, key string, r io.Reader, _ int64, contentType string) error {
	if f.putErr != nil {
		return f.putErr
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.puts[key] = data
	f.putTypes[key] = contentType
	return nil
}

func (f *fakeStore) Delete(context.Context, string) error { return nil }

type fakeEnqueuer struct {
	mu   sync.Mutex
	jobs []queue.Job
}

func (f *fakeEnqueuer) EnqueueJob(_ context.Context, job queue.Job) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.jobs = append(f.jobs, job)
	return nil
}

type fakeSender struct {
	mu   sync.Mutex
	sent []sentEmail
	err  error
}

type sentEmail struct {
	To       string
	Template string
	Payload  email.DeliveryPayload
}

func (f *fakeSender) Send(_ context.Context, to, template string, payload email.DeliveryPayload) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, sentEmail{To: to, Template: template, Payload: payload})
	return f.err
}

func serviceFixture(t *testing.T) (*Service, *fakeRepo, *fakeStore, *fakeEnqueuer, uuid.UUID, uuid.UUID) {
	t.Helper()
	repo := newFakeRepo()
	store := newFakeStore()
	enq := &fakeEnqueuer{}
	orgID := uuid.New()
	userID := uuid.New()
	svc := NewService(repo, store, 24*time.Hour)
	svc.SetEnqueuer(enq)
	return svc, repo, store, enq, orgID, userID
}

func TestCreateEnqueuesExport(t *testing.T) {
	svc, repo, _, enq, orgID, userID := serviceFixture(t)
	dto, err := svc.Create(context.Background(), orgID, userID, CreateRequest{
		Status: "open", Priority: "high",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if dto.Status != StatusPending {
		t.Fatalf("status=%s", dto.Status)
	}
	if len(enq.jobs) != 1 {
		t.Fatalf("jobs=%d", len(enq.jobs))
	}
	job := enq.jobs[0]
	if job.Type != queue.TaskExportRun || job.Queue != queue.QueueLow {
		t.Fatalf("job=%+v", job)
	}
	id, _ := uuid.Parse(dto.ID)
	if job.ID != queue.TaskExportRun+":"+id.String() {
		t.Fatalf("job id=%q", job.ID)
	}
	var payload TaskPayload
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if payload.JobID != id || payload.OrgID != orgID {
		t.Fatalf("payload=%+v", payload)
	}
	stored := repo.jobs[id]
	var f Filter
	if err := json.Unmarshal(stored.Filters, &f); err != nil {
		t.Fatalf("filters: %v", err)
	}
	if f.Status == nil || *f.Status != "open" || f.Priority == nil || *f.Priority != "high" {
		t.Fatalf("filters=%+v", f)
	}
}

func TestCreateRejectsInvalidFilters(t *testing.T) {
	cases := []CreateRequest{
		{Status: "bogus"},
		{Priority: "critical"},
		{AssigneeID: "not-a-uuid"},
		{RequesterID: "not-a-uuid"},
		{CreatedFrom: "yesterday"},
		{CreatedTo: "tomorrow"},
		{CreatedFrom: "2026-02-01T00:00:00Z", CreatedTo: "2026-01-01T00:00:00Z"},
	}
	for i, req := range cases {
		svc, repo, _, enq, orgID, userID := serviceFixture(t)
		if _, err := svc.Create(context.Background(), orgID, userID, req); err == nil {
			t.Fatalf("case %d accepted invalid filter %+v", i, req)
		}
		if len(repo.jobs) != 0 || len(enq.jobs) != 0 {
			t.Fatalf("case %d persisted work for invalid filter", i)
		}
	}
}

func TestGetScopesToOrgAndSignsDone(t *testing.T) {
	svc, repo, _, _, orgID, userID := serviceFixture(t)
	dto, err := svc.Create(context.Background(), orgID, userID, CreateRequest{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	id, _ := uuid.Parse(dto.ID)

	// Pending exports carry no URL.
	got, err := svc.Get(context.Background(), orgID, id)
	if err != nil {
		t.Fatalf("get pending: %v", err)
	}
	if got.DownloadURL != "" || got.ExpiresAt != nil {
		t.Fatalf("pending export has a link: %+v", got)
	}

	// Foreign org sees not found, never forbidden.
	if _, err := svc.Get(context.Background(), uuid.New(), id); !errors.Is(err, ErrExportNotFound) {
		t.Fatalf("foreign get err=%v", err)
	}

	job := repo.jobs[id]
	job.Status = StatusDone
	job.StorageKey = ExportKey(orgID, id)
	repo.jobs[id] = job

	got, err = svc.Get(context.Background(), orgID, id)
	if err != nil {
		t.Fatalf("get done: %v", err)
	}
	if got.DownloadURL == "" || got.ExpiresAt == nil {
		t.Fatalf("done export missing link: %+v", got)
	}
	if got.ExpiresAt.Sub(time.Now()) < 23*time.Hour {
		t.Fatalf("link ttl too short: %s", got.ExpiresAt.Sub(time.Now()))
	}
}

func sampleRows(orgID uuid.UUID, n int) []TicketRow {
	base := time.Now().UTC().Truncate(time.Second)
	out := make([]TicketRow, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, TicketRow{
			ID:             uuid.New(),
			Number:         int64(1000 + i),
			Subject:        "Subject, with comma " + strconv.Itoa(i),
			Status:         "open",
			Priority:       "normal",
			Channel:        "api",
			RequesterEmail: "customer@example.test",
			RequesterName:  "Customer",
			AssigneeEmail:  "agent@example.test",
			CreatedAt:      base.Add(-time.Duration(i) * time.Second),
			UpdatedAt:      base.Add(-time.Duration(i) * time.Second),
		})
	}
	return out
}

func taskFixture(t *testing.T, rows []TicketRow) (*TaskHandler, *fakeRepo, *fakeStore, *fakeSender, uuid.UUID, uuid.UUID) {
	t.Helper()
	repo := newFakeRepo()
	store := newFakeStore()
	sender := &fakeSender{}
	orgID := uuid.New()
	userID := uuid.New()
	repo.rows = rows
	repo.rowOrg = orgID
	repo.recEmail = "requester@example.test"
	repo.orgName = "Acme"
	job, err := repo.CreateJob(context.Background(), orgID, userID, []byte(`{}`))
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	handler := NewTaskHandler(repo, store, sender, 2, 24*time.Hour, nil)
	return handler, repo, store, sender, orgID, job.ID
}

func taskFor(t *testing.T, orgID, jobID uuid.UUID) *asynq.Task {
	t.Helper()
	payload, err := json.Marshal(TaskPayload{JobID: jobID, OrgID: orgID})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return asynq.NewTask(queue.TaskExportRun, payload)
}

func TestRunStreamsCSVAndNotifies(t *testing.T) {
	rows := sampleRows(uuid.New(), 5)
	h, repo, store, sender, orgID, jobID := taskFixture(t, rows)

	if err := h.Run(context.Background(), taskFor(t, orgID, jobID)); err != nil {
		t.Fatalf("run: %v", err)
	}

	job := repo.jobs[jobID]
	if job.Status != StatusDone {
		t.Fatalf("status=%s error=%s", job.Status, job.Error)
	}
	if job.RowCount == nil || *job.RowCount != 5 {
		t.Fatalf("row count=%v", job.RowCount)
	}
	key := ExportKey(orgID, jobID)
	if job.StorageKey != key {
		t.Fatalf("key=%q", job.StorageKey)
	}
	data, ok := store.puts[key]
	if !ok {
		t.Fatal("csv object not uploaded")
	}
	if store.putTypes[key] != "text/csv" {
		t.Fatalf("content type=%q", store.putTypes[key])
	}
	records, err := csv.NewReader(bytes.NewReader(data)).ReadAll()
	if err != nil {
		t.Fatalf("parse csv: %v", err)
	}
	if len(records) != 6 {
		t.Fatalf("records=%d want 6", len(records))
	}
	if len(records[0]) != len(CSVHeader) {
		t.Fatalf("header=%v", records[0])
	}
	if records[1][0] != "1000" || records[1][1] != rows[0].Subject {
		t.Fatalf("first row=%v", records[1])
	}
	if records[5][0] != "1004" {
		t.Fatalf("last row=%v", records[5])
	}

	if len(sender.sent) != 1 {
		t.Fatalf("sent=%d", len(sender.sent))
	}
	msg := sender.sent[0]
	if msg.To != "requester@example.test" || msg.Template != email.TemplateExportReady {
		t.Fatalf("email=%+v", msg)
	}
	if msg.Payload.ExportRows != 5 {
		t.Fatalf("rows=%d", msg.Payload.ExportRows)
	}
	if msg.Payload.ExportURL == "" || msg.Payload.OrgName != "Acme" {
		t.Fatalf("payload=%+v", msg.Payload)
	}
}

func TestRunIsIdempotentWhenDone(t *testing.T) {
	h, repo, store, _, orgID, jobID := taskFixture(t, sampleRows(uuid.New(), 2))
	job := repo.jobs[jobID]
	job.Status = StatusDone
	repo.jobs[jobID] = job

	if err := h.Run(context.Background(), taskFor(t, orgID, jobID)); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(store.puts) != 0 {
		t.Fatal("done export was rebuilt")
	}
}

func TestRunForeignOrgDoesNothing(t *testing.T) {
	h, _, store, sender, _, jobID := taskFixture(t, sampleRows(uuid.New(), 2))
	if err := h.Run(context.Background(), taskFor(t, uuid.New(), jobID)); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(store.puts) != 0 || len(sender.sent) != 0 {
		t.Fatal("foreign export produced work")
	}
}

func TestRunSourceErrorMarksFailed(t *testing.T) {
	h, repo, _, _, orgID, jobID := taskFixture(t, nil)
	repo.listErr = errors.New("database down")

	if err := h.Run(context.Background(), taskFor(t, orgID, jobID)); err == nil {
		t.Fatal("expected error")
	}
	job := repo.jobs[jobID]
	if job.Status != StatusFailed || job.Error == "" {
		t.Fatalf("job=%+v", job)
	}
}

func TestRunStorageErrorMarksFailed(t *testing.T) {
	h, repo, store, _, orgID, jobID := taskFixture(t, sampleRows(uuid.New(), 3))
	store.putErr = errors.New("storage down")

	if err := h.Run(context.Background(), taskFor(t, orgID, jobID)); err == nil {
		t.Fatal("expected error")
	}
	if repo.jobs[jobID].Status != StatusFailed {
		t.Fatalf("status=%s", repo.jobs[jobID].Status)
	}
}

func TestRunBadPayloadIsPermanent(t *testing.T) {
	h, _, _, _, _, _ := taskFixture(t, nil)
	if err := h.Run(context.Background(), asynq.NewTask(queue.TaskExportRun, []byte("{"))); err == nil {
		t.Fatal("expected error")
	}
}
