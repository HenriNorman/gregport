package attachments

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/HenriNorman/gregport/internal/queue"
	"github.com/HenriNorman/gregport/internal/storage"
)

type fakeRepo struct {
	mu          sync.Mutex
	tickets     map[string]uuid.UUID // orgID:number
	attachments map[uuid.UUID]Attachment
	inserted    int
	deleted     []uuid.UUID
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		tickets:     make(map[string]uuid.UUID),
		attachments: make(map[uuid.UUID]Attachment),
	}
}

func ticketKey(orgID uuid.UUID, number int64) string {
	return orgID.String() + ":" + strconv.FormatInt(number, 10)
}

func (f *fakeRepo) seedTicket(orgID uuid.UUID, number int64) uuid.UUID {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := uuid.New()
	f.tickets[ticketKey(orgID, number)] = id
	return id
}

func (f *fakeRepo) GetTicketIDByNumber(_ context.Context, orgID uuid.UUID, number int64) (uuid.UUID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id, ok := f.tickets[ticketKey(orgID, number)]
	if !ok {
		return uuid.Nil, ErrTicketNotFound
	}
	return id, nil
}

func (f *fakeRepo) InsertAttachment(_ context.Context, a Attachment) (Attachment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a.ID = uuid.New()
	a.UploadStatus = StatusPending
	a.CreatedAt = time.Now().UTC()
	f.attachments[a.ID] = a
	f.inserted++
	return a, nil
}

func (f *fakeRepo) GetAttachment(_ context.Context, orgID, id uuid.UUID) (Attachment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.attachments[id]
	if !ok || a.OrgID != orgID {
		return Attachment{}, ErrAttachmentNotFound
	}
	return a, nil
}

func (f *fakeRepo) MarkUploaded(_ context.Context, orgID, id uuid.UUID) (Attachment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.attachments[id]
	if !ok || a.OrgID != orgID {
		return Attachment{}, ErrAttachmentNotFound
	}
	a.UploadStatus = StatusUploaded
	now := time.Now().UTC()
	a.ConfirmedAt = &now
	a.ExpiresAt = nil
	f.attachments[id] = a
	return a, nil
}

func (f *fakeRepo) MarkQuarantined(_ context.Context, _ uuid.UUID, id uuid.UUID, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := f.attachments[id]
	a.UploadStatus = StatusQuarantined
	a.QuarantineReason = reason
	f.attachments[id] = a
	return nil
}

func (f *fakeRepo) SetThumbnail(_ context.Context, _ uuid.UUID, id uuid.UUID, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := f.attachments[id]
	a.ThumbnailKey = key
	f.attachments[id] = a
	return nil
}

func (f *fakeRepo) SetChecksum(_ context.Context, _ uuid.UUID, id uuid.UUID, checksum string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := f.attachments[id]
	a.Checksum = checksum
	f.attachments[id] = a
	return nil
}

func (f *fakeRepo) ListExpiredPending(_ context.Context, before time.Time, limit int) ([]Attachment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Attachment, 0)
	for _, a := range f.attachments {
		if a.UploadStatus == StatusPending && a.ExpiresAt != nil && a.ExpiresAt.Before(before) {
			out = append(out, a)
			if len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func (f *fakeRepo) DeleteAttachments(_ context.Context, ids []uuid.UUID) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var n int64
	for _, id := range ids {
		if _, ok := f.attachments[id]; ok {
			delete(f.attachments, id)
			f.deleted = append(f.deleted, id)
			n++
		}
	}
	return n, nil
}

func (f *fakeRepo) ListKeysForTicket(_ context.Context, orgID, ticketID uuid.UUID) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var keys []string
	for _, a := range f.attachments {
		if a.OrgID == orgID && a.TicketID == ticketID {
			keys = append(keys, a.StorageKey)
			if a.ThumbnailKey != "" {
				keys = append(keys, a.ThumbnailKey)
			}
		}
	}
	return keys, nil
}

type fakeObject struct {
	data        []byte
	contentType string
	etag        string
}

type fakeStore struct {
	mu      sync.Mutex
	objects map[string]fakeObject
	headErr error
	putErr  error
	puts    map[string][]byte
	putType map[string]string
	deleted []string
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		objects: make(map[string]fakeObject),
		puts:    make(map[string][]byte),
		putType: make(map[string]string),
	}
}

func (f *fakeStore) putObject(key string, data []byte, contentType string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[key] = fakeObject{data: data, contentType: contentType, etag: "etag-" + key}
}

func (f *fakeStore) PresignPut(_ context.Context, key string, _ time.Duration) (string, error) {
	return "https://storage.test/put/" + key, nil
}

func (f *fakeStore) PresignGet(_ context.Context, key string, _ time.Duration, disposition string) (string, error) {
	return "https://storage.test/get/" + key + "?disposition=" + disposition, nil
}

func (f *fakeStore) Head(_ context.Context, key string) (storage.ObjectInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.headErr != nil {
		return storage.ObjectInfo{}, f.headErr
	}
	obj, ok := f.objects[key]
	if !ok {
		return storage.ObjectInfo{}, storage.ErrObjectNotFound
	}
	return storage.ObjectInfo{Key: key, Size: int64(len(obj.data)), ContentType: obj.contentType, ETag: obj.etag}, nil
}

func (f *fakeStore) GetRange(_ context.Context, key string, n int64) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	obj, ok := f.objects[key]
	if !ok {
		return nil, storage.ErrObjectNotFound
	}
	if int64(len(obj.data)) < n {
		return obj.data, nil
	}
	return obj.data[:n], nil
}

func (f *fakeStore) Open(_ context.Context, key string) (io.ReadCloser, storage.ObjectInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	obj, ok := f.objects[key]
	if !ok {
		return nil, storage.ObjectInfo{}, storage.ErrObjectNotFound
	}
	return io.NopCloser(bytes.NewReader(obj.data)), storage.ObjectInfo{
		Key: key, Size: int64(len(obj.data)), ContentType: obj.contentType,
	}, nil
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
	f.putType[key] = contentType
	f.objects[key] = fakeObject{data: data, contentType: contentType, etag: "etag-" + key}
	return nil
}

func (f *fakeStore) Delete(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.objects, key)
	f.deleted = append(f.deleted, key)
	return nil
}

type fakeEnqueuer struct {
	mu   sync.Mutex
	jobs []queue.Job
	err  error
}

func (f *fakeEnqueuer) EnqueueJob(_ context.Context, job queue.Job) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.jobs = append(f.jobs, job)
	return f.err
}

func (f *fakeEnqueuer) types() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.jobs))
	for _, j := range f.jobs {
		out = append(out, j.Type)
	}
	return out
}

var _ = errors.Is
