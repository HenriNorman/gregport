package attachments

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"

	"github.com/HenriNorman/gregport/internal/queue"
)

func uploadedFixture(t *testing.T, filename, mime string, data []byte) (*TaskHandler, *fakeRepo, *fakeStore, Attachment) {
	t.Helper()
	repo := newFakeRepo()
	store := newFakeStore()
	orgID := uuid.New()
	repo.seedTicket(orgID, 1042)
	svc := NewService(repo, store, 10<<20, 0, 0, 0)
	res, err := svc.Presign(context.Background(), orgID, 1042, PresignRequest{
		Filename: filename, MIMEType: mime, Size: int64(len(data)),
	})
	if err != nil {
		t.Fatalf("presign: %v", err)
	}
	id, _ := uuid.Parse(res.AttachmentID)
	att := repo.attachments[id]
	store.putObject(att.StorageKey, data, mime)
	att, err = repo.MarkUploaded(context.Background(), orgID, id)
	if err != nil {
		t.Fatalf("mark uploaded: %v", err)
	}
	handler := NewTaskHandler(repo, store, 128, nil)
	return handler, repo, store, att
}

func taskFor(t *testing.T, taskType string, att Attachment) *asynq.Task {
	t.Helper()
	payload, _ := json.Marshal(jobPayload{AttachmentID: att.ID, OrgID: att.OrgID})
	return asynq.NewTask(taskType, payload)
}

func TestValidateMatchingTypeStaysUploaded(t *testing.T) {
	h, repo, _, att := uploadedFixture(t, "doc.pdf", "application/pdf", []byte("%PDF-1.4\nhello"))
	if err := h.Validate(context.Background(), taskFor(t, queue.TaskAttachmentValidate, att)); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if repo.attachments[att.ID].UploadStatus != StatusUploaded {
		t.Fatalf("status=%s", repo.attachments[att.ID].UploadStatus)
	}
}

func TestValidateMismatchQuarantines(t *testing.T) {
	// Declared as an image but starts with the MS-DOS executable magic.
	h, repo, _, att := uploadedFixture(t, "photo.png", "image/png", []byte("MZ\x90\x00\x03"))
	if err := h.Validate(context.Background(), taskFor(t, queue.TaskAttachmentValidate, att)); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if repo.attachments[att.ID].UploadStatus != StatusQuarantined {
		t.Fatalf("status=%s want quarantined", repo.attachments[att.ID].UploadStatus)
	}
	if repo.attachments[att.ID].QuarantineReason == "" {
		t.Fatal("missing quarantine reason")
	}
}

func TestValidateExecutableEvenWhenDeclaredText(t *testing.T) {
	h, repo, _, att := uploadedFixture(t, "notes.txt", "text/plain", []byte("MZ\x90\x00\x03"))
	if err := h.Validate(context.Background(), taskFor(t, queue.TaskAttachmentValidate, att)); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if repo.attachments[att.ID].UploadStatus != StatusQuarantined {
		t.Fatalf("status=%s want quarantined", repo.attachments[att.ID].UploadStatus)
	}
}

func TestValidateMissingObjectQuarantines(t *testing.T) {
	h, repo, store, att := uploadedFixture(t, "doc.pdf", "application/pdf", []byte("%PDF-1.4\n"))
	store.mu.Lock()
	delete(store.objects, att.StorageKey)
	store.mu.Unlock()
	if err := h.Validate(context.Background(), taskFor(t, queue.TaskAttachmentValidate, att)); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if repo.attachments[att.ID].UploadStatus != StatusQuarantined {
		t.Fatalf("status=%s want quarantined", repo.attachments[att.ID].UploadStatus)
	}
}

func TestValidateCsvIsCompatibleWithPlainText(t *testing.T) {
	h, repo, _, att := uploadedFixture(t, "rows.csv", "text/csv", []byte("a,b\n1,2\n"))
	if err := h.Validate(context.Background(), taskFor(t, queue.TaskAttachmentValidate, att)); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if repo.attachments[att.ID].UploadStatus != StatusUploaded {
		t.Fatalf("status=%s", repo.attachments[att.ID].UploadStatus)
	}
}

func TestThumbnailGeneratesObject(t *testing.T) {
	h, repo, store, att := uploadedFixture(t, "photo.png", "image/png", pngBytes(t))
	if err := h.Thumbnail(context.Background(), taskFor(t, queue.TaskThumbnailGenerate, att)); err != nil {
		t.Fatalf("thumbnail: %v", err)
	}
	updated := repo.attachments[att.ID]
	if updated.ThumbnailKey == "" {
		t.Fatal("thumbnail key not stored")
	}
	store.mu.Lock()
	obj, ok := store.objects[updated.ThumbnailKey]
	store.mu.Unlock()
	if !ok {
		t.Fatal("thumbnail object not uploaded")
	}
	if obj.contentType != "image/jpeg" {
		t.Fatalf("content type=%q", obj.contentType)
	}
	if len(obj.data) == 0 {
		t.Fatal("empty thumbnail")
	}
}

func TestThumbnailSkipsNonImage(t *testing.T) {
	h, repo, _, att := uploadedFixture(t, "doc.pdf", "application/pdf", []byte("%PDF-1.4\n"))
	if err := h.Thumbnail(context.Background(), taskFor(t, queue.TaskThumbnailGenerate, att)); err != nil {
		t.Fatalf("thumbnail: %v", err)
	}
	if repo.attachments[att.ID].ThumbnailKey != "" {
		t.Fatal("unexpected thumbnail for non-image")
	}
}

func pngBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 64, 32))
	for y := 0; y < 32; y++ {
		for x := 0; x < 64; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 4), G: uint8(y * 8), B: 128, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}
