package attachments

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func fixture(t *testing.T) (*Service, *fakeRepo, *fakeStore, *fakeEnqueuer, uuid.UUID, uuid.UUID) {
	t.Helper()
	repo := newFakeRepo()
	store := newFakeStore()
	enq := &fakeEnqueuer{}
	orgID := uuid.New()
	ticketID := repo.seedTicket(orgID, 1042)
	svc := NewService(repo, store, 10<<20, 5*time.Minute, 2*time.Minute, time.Hour)
	svc.SetEnqueuer(enq)
	return svc, repo, store, enq, orgID, ticketID
}

func TestPresignCreatesPendingServerKey(t *testing.T) {
	svc, repo, store, _, orgID, ticketID := fixture(t)
	res, err := svc.Presign(context.Background(), orgID, 1042, PresignRequest{
		Filename: "receipt.pdf", MIMEType: "application/pdf", Size: 1234,
	})
	if err != nil {
		t.Fatalf("presign: %v", err)
	}
	if res.UploadURL == "" || res.AttachmentID == "" {
		t.Fatalf("incomplete response: %+v", res)
	}
	id, _ := uuid.Parse(res.AttachmentID)
	att := repo.attachments[id]
	if att.UploadStatus != StatusPending {
		t.Fatalf("status=%s", att.UploadStatus)
	}
	if !strings.HasPrefix(att.StorageKey, "org/"+orgID.String()+"/tickets/"+ticketID.String()+"/") {
		t.Fatalf("key=%q is not tenant/ticket prefixed", att.StorageKey)
	}
	if strings.Contains(att.StorageKey, "receipt") || strings.Contains(att.StorageKey, "..") {
		t.Fatalf("client filename leaked into key: %q", att.StorageKey)
	}
	if !strings.Contains(res.UploadURL, att.StorageKey) {
		t.Fatalf("upload url not scoped to key: %q", res.UploadURL)
	}
	_ = store
}

func TestPresignValidationTable(t *testing.T) {
	svc, repo, _, _, orgID, _ := fixture(t)
	cases := []struct {
		name string
		req  PresignRequest
		want error
	}{
		{"oversize", PresignRequest{"a.pdf", "application/pdf", 50 << 20}, ErrFileTooLarge},
		{"disallowed type", PresignRequest{"a.exe", "application/x-msdownload", 10}, ErrUnsupportedType},
		{"empty type", PresignRequest{"a.pdf", "", 10}, ErrInvalidFile},
		{"zero size", PresignRequest{"a.pdf", "application/pdf", 0}, ErrInvalidFile},
		{"dotfile", PresignRequest{".env", "text/plain", 10}, ErrInvalidFilename},
		{"empty filename", PresignRequest{"   ", "text/plain", 10}, ErrInvalidFilename},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := svc.Presign(context.Background(), orgID, 1042, c.req)
			if !errors.Is(err, c.want) {
				t.Fatalf("err=%v want %v", err, c.want)
			}
		})
	}
	if repo.inserted != 0 {
		t.Fatalf("rejected presigns created %d rows", repo.inserted)
	}
}

func TestPresignHostileFilenameIsSanitized(t *testing.T) {
	svc, repo, _, _, orgID, _ := fixture(t)
	res, err := svc.Presign(context.Background(), orgID, 1042, PresignRequest{
		Filename: "../../etc/passwd", MIMEType: "text/plain", Size: 10,
	})
	if err != nil {
		t.Fatalf("presign: %v", err)
	}
	id, _ := uuid.Parse(res.AttachmentID)
	att := repo.attachments[id]
	if att.Filename != "passwd" {
		t.Fatalf("filename=%q", att.Filename)
	}
	if strings.Contains(att.StorageKey, "..") || strings.Contains(att.StorageKey, "passwd") {
		t.Fatalf("hostile name leaked into key: %q", att.StorageKey)
	}
}

func TestPresignForeignTicket(t *testing.T) {
	svc, _, _, _, orgID, _ := fixture(t)
	_, err := svc.Presign(context.Background(), orgID, 9999, PresignRequest{
		Filename: "a.pdf", MIMEType: "application/pdf", Size: 10,
	})
	if !errors.Is(err, ErrTicketNotFound) {
		t.Fatalf("err=%v want ticket not found", err)
	}
}

func TestConfirmMissingObjectKeepsPending(t *testing.T) {
	svc, repo, store, _, orgID, _ := fixture(t)
	res, _ := svc.Presign(context.Background(), orgID, 1042, PresignRequest{
		Filename: "a.pdf", MIMEType: "application/pdf", Size: 10,
	})
	id, _ := uuid.Parse(res.AttachmentID)
	_ = store
	_, err := svc.Confirm(context.Background(), orgID, id)
	if !errors.Is(err, ErrUploadNotFound) {
		t.Fatalf("err=%v want upload not found", err)
	}
	if repo.attachments[id].UploadStatus != StatusPending {
		t.Fatalf("status changed to %s", repo.attachments[id].UploadStatus)
	}
}

func TestConfirmSizeMismatchKeepsPending(t *testing.T) {
	svc, repo, store, _, orgID, _ := fixture(t)
	res, _ := svc.Presign(context.Background(), orgID, 1042, PresignRequest{
		Filename: "a.pdf", MIMEType: "application/pdf", Size: 10,
	})
	id, _ := uuid.Parse(res.AttachmentID)
	att := repo.attachments[id]
	store.putObject(att.StorageKey, []byte("too long"), "application/pdf")

	_, err := svc.Confirm(context.Background(), orgID, id)
	if !errors.Is(err, ErrSizeMismatch) {
		t.Fatalf("err=%v want size mismatch", err)
	}
	if repo.attachments[id].UploadStatus != StatusPending {
		t.Fatalf("status changed to %s", repo.attachments[id].UploadStatus)
	}
}

func TestConfirmSuccessEnqueuesWork(t *testing.T) {
	svc, repo, store, enq, orgID, _ := fixture(t)
	res, _ := svc.Presign(context.Background(), orgID, 1042, PresignRequest{
		Filename: "photo.png", MIMEType: "image/png", Size: 4,
	})
	id, _ := uuid.Parse(res.AttachmentID)
	store.putObject(repo.attachments[id].StorageKey, []byte("PNG!"), "image/png")

	att, err := svc.Confirm(context.Background(), orgID, id)
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if att.UploadStatus != StatusUploaded {
		t.Fatalf("status=%s", att.UploadStatus)
	}
	got := enq.types()
	if len(got) != 2 {
		t.Fatalf("jobs=%v want validate+thumbnail", got)
	}
	if got[0] != "attachment:validate" || got[1] != "thumbnail:generate" {
		t.Fatalf("jobs=%v", got)
	}

	// Confirming again is idempotent and does not re-enqueue.
	if _, err := svc.Confirm(context.Background(), orgID, id); err != nil {
		t.Fatalf("second confirm: %v", err)
	}
	if len(enq.types()) != 2 {
		t.Fatalf("duplicate jobs: %v", enq.types())
	}
}

func TestConfirmNonImageDoesNotEnqueueThumbnail(t *testing.T) {
	svc, repo, store, enq, orgID, _ := fixture(t)
	res, _ := svc.Presign(context.Background(), orgID, 1042, PresignRequest{
		Filename: "a.pdf", MIMEType: "application/pdf", Size: 4,
	})
	id, _ := uuid.Parse(res.AttachmentID)
	store.putObject(repo.attachments[id].StorageKey, []byte("PDF!"), "application/pdf")
	if _, err := svc.Confirm(context.Background(), orgID, id); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	got := enq.types()
	if len(got) != 1 || got[0] != "attachment:validate" {
		t.Fatalf("jobs=%v", got)
	}
}

func TestDownloadScopedToTenant(t *testing.T) {
	svc, repo, store, _, orgID, _ := fixture(t)
	res, _ := svc.Presign(context.Background(), orgID, 1042, PresignRequest{
		Filename: "a.pdf", MIMEType: "application/pdf", Size: 4,
	})
	id, _ := uuid.Parse(res.AttachmentID)
	store.putObject(repo.attachments[id].StorageKey, []byte("PDF!"), "application/pdf")
	if _, err := svc.Confirm(context.Background(), orgID, id); err != nil {
		t.Fatalf("confirm: %v", err)
	}

	other := uuid.New()
	if _, err := svc.Download(context.Background(), other, id); !errors.Is(err, ErrAttachmentNotFound) {
		t.Fatalf("foreign download err=%v", err)
	}

	dl, err := svc.Download(context.Background(), orgID, id)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if !strings.Contains(dl.ContentDisposition, "attachment") {
		t.Fatalf("disposition=%q", dl.ContentDisposition)
	}
	if !strings.Contains(dl.URL, "storage.test") {
		t.Fatalf("url=%q", dl.URL)
	}
}

func TestDownloadImageIsInline(t *testing.T) {
	svc, repo, store, _, orgID, _ := fixture(t)
	res, _ := svc.Presign(context.Background(), orgID, 1042, PresignRequest{
		Filename: "photo.png", MIMEType: "image/png", Size: 4,
	})
	id, _ := uuid.Parse(res.AttachmentID)
	store.putObject(repo.attachments[id].StorageKey, []byte("PNG!"), "image/png")
	if _, err := svc.Confirm(context.Background(), orgID, id); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	dl, err := svc.Download(context.Background(), orgID, id)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if !strings.HasPrefix(dl.ContentDisposition, "inline") {
		t.Fatalf("disposition=%q", dl.ContentDisposition)
	}
}

func TestDownloadPendingRefused(t *testing.T) {
	svc, _, _, _, orgID, _ := fixture(t)
	res, _ := svc.Presign(context.Background(), orgID, 1042, PresignRequest{
		Filename: "a.pdf", MIMEType: "application/pdf", Size: 4,
	})
	id, _ := uuid.Parse(res.AttachmentID)
	if _, err := svc.Download(context.Background(), orgID, id); !errors.Is(err, ErrAttachmentNotReady) {
		t.Fatalf("err=%v want not ready", err)
	}
}
