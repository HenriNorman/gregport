package attachments

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/HenriNorman/gregport/internal/storage"
)

// PurgeExpired deletes pending attachments whose upload window elapsed, along
// with any object the client managed to upload. Objects are removed first so a
// storage outage leaves the rows in place and the next run retries safely.
func PurgeExpired(ctx context.Context, repo Store, store storage.Backend, before time.Time, batch int) (int64, error) {
	if batch <= 0 {
		batch = 100
	}
	expired, err := repo.ListExpiredPending(ctx, before, batch)
	if err != nil {
		return 0, err
	}
	if len(expired) == 0 {
		return 0, nil
	}

	ids := make([]uuid.UUID, 0, len(expired))
	for _, att := range expired {
		if att.StorageKey != "" {
			if err := store.Delete(ctx, att.StorageKey); err != nil {
				return 0, fmt.Errorf("attachments: delete orphan object %q: %w", att.StorageKey, err)
			}
		}
		ids = append(ids, att.ID)
	}
	return repo.DeleteAttachments(ctx, ids)
}
