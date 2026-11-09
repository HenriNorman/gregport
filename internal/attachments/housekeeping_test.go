package attachments

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestPurgeExpiredRemovesObjectsAndRows(t *testing.T) {
	repo := newFakeRepo()
	store := newFakeStore()
	orgID := uuid.New()

	expiredAt := time.Now().UTC().Add(-2 * time.Hour)
	expired := Attachment{
		ID:           uuid.New(),
		OrgID:        orgID,
		TicketID:     uuid.New(),
		StorageKey:   "org/" + orgID.String() + "/tickets/x/expired",
		Filename:     "expired.pdf",
		MIMEType:     "application/pdf",
		SizeBytes:    10,
		UploadStatus: StatusPending,
		ExpiresAt:    &expiredAt,
		CreatedAt:    expiredAt,
	}
	freshAt := time.Now().UTC().Add(time.Hour)
	fresh := Attachment{
		ID:           uuid.New(),
		OrgID:        orgID,
		TicketID:     uuid.New(),
		StorageKey:   "org/" + orgID.String() + "/tickets/x/fresh",
		Filename:     "fresh.pdf",
		MIMEType:     "application/pdf",
		SizeBytes:    10,
		UploadStatus: StatusPending,
		ExpiresAt:    &freshAt,
		CreatedAt:    time.Now().UTC(),
	}
	repo.attachments[expired.ID] = expired
	repo.attachments[fresh.ID] = fresh
	store.putObject(expired.StorageKey, []byte("orphan"), "application/pdf")

	// before = now - 1h, so only the row expired at now-2h qualifies.
	purged, err := PurgeExpired(context.Background(), repo, store, time.Now().UTC().Add(-time.Hour), 10)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if purged != 1 {
		t.Fatalf("purged=%d want 1", purged)
	}
	if _, ok := repo.attachments[expired.ID]; ok {
		t.Fatal("expired attachment row remains")
	}
	if _, ok := repo.attachments[fresh.ID]; !ok {
		t.Fatal("fresh attachment was purged")
	}
	store.mu.Lock()
	_, objectRemains := store.objects[expired.StorageKey]
	store.mu.Unlock()
	if objectRemains {
		t.Fatal("orphan object was not deleted")
	}
}

func TestPurgeExpiredNoRows(t *testing.T) {
	repo := newFakeRepo()
	store := newFakeStore()
	purged, err := PurgeExpired(context.Background(), repo, store, time.Now(), 10)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if purged != 0 {
		t.Fatalf("purged=%d", purged)
	}
}
