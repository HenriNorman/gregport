package exports

import (
	"context"
	"io"
	"runtime"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/HenriNorman/gregport/internal/storage"
)

// discardStore implements storage.Backend but throws uploaded bytes away, so
// the memory test measures the streaming pipeline rather than the sink.
type discardStore struct {
	bytes int64
}

func (d *discardStore) PresignPut(context.Context, string, time.Duration) (string, error) {
	return "", nil
}
func (d *discardStore) PresignGet(context.Context, string, time.Duration, string) (string, error) {
	return "https://storage.test/export.csv", nil
}
func (d *discardStore) Head(context.Context, string) (storage.ObjectInfo, error) {
	return storage.ObjectInfo{}, storage.ErrObjectNotFound
}
func (d *discardStore) GetRange(context.Context, string, int64) ([]byte, error) {
	return nil, storage.ErrObjectNotFound
}
func (d *discardStore) Open(context.Context, string) (io.ReadCloser, storage.ObjectInfo, error) {
	return nil, storage.ObjectInfo{}, storage.ErrObjectNotFound
}
func (d *discardStore) Put(_ context.Context, _ string, r io.Reader, _ int64, _ string) error {
	n, err := io.Copy(io.Discard, r)
	d.bytes += n
	return err
}
func (d *discardStore) Delete(context.Context, string) error { return nil }

// TestExportHundredThousandRowsFlatMemory exports 100,000 generated rows and
// asserts the resident heap stays bounded. The row generator is demand-driven
// and the sink discards bytes, so any growth proportional to the export size
// would show up as a large heap delta.
func TestExportHundredThousandRowsFlatMemory(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping 100k-row export in short mode")
	}

	const total = 100000
	base := time.Now().UTC().Truncate(time.Second)
	orgID := uuid.New()
	userID := uuid.New()

	repo := newFakeRepo()
	repo.recEmail = "requester@example.test"
	repo.orgName = "Acme"
	repo.generator = func(after time.Time, limit int) []TicketRow {
		start := 0
		if !after.IsZero() {
			start = int(base.Sub(after)/time.Second) + 1
		}
		if start >= total {
			return nil
		}
		end := start + limit
		if end > total {
			end = total
		}
		out := make([]TicketRow, 0, end-start)
		for i := start; i < end; i++ {
			out = append(out, TicketRow{
				ID:             uuid.New(),
				Number:         int64(total - i),
				Subject:        "Ticket subject",
				Status:         "open",
				Priority:       "normal",
				Channel:        "api",
				RequesterEmail: "customer@example.test",
				RequesterName:  "Customer",
				CreatedAt:      base.Add(-time.Duration(i) * time.Second),
				UpdatedAt:      base.Add(-time.Duration(i) * time.Second),
			})
		}
		return out
	}

	job, err := repo.CreateJob(context.Background(), orgID, userID, []byte(`{}`))
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	sink := &discardStore{}
	handler := NewTaskHandler(repo, sink, nil, 1000, time.Hour, nil)

	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	if err := handler.Run(context.Background(), taskFor(t, orgID, job.ID)); err != nil {
		t.Fatalf("run: %v", err)
	}

	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)

	done := repo.jobs[job.ID]
	if done.RowCount == nil || *done.RowCount != total {
		t.Fatalf("row count=%v want %d", done.RowCount, total)
	}
	if sink.bytes == 0 {
		t.Fatal("no CSV bytes streamed")
	}
	// 100k rows of data must not remain resident. Allow generous headroom for
	// test runtime and GC metadata.
	const maxGrowth = 32 << 20
	if growth := int64(after.HeapAlloc) - int64(before.HeapAlloc); growth > maxGrowth {
		t.Fatalf("heap grew by %d bytes, want <= %d", growth, maxGrowth)
	}
}
