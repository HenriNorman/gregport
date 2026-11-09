package exports

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"

	"github.com/HenriNorman/gregport/internal/email"
	"github.com/HenriNorman/gregport/internal/queue"
	"github.com/HenriNorman/gregport/internal/storage"
)

// TaskPayload tells the worker which export job to build.
type TaskPayload struct {
	JobID uuid.UUID `json:"job_id"`
	OrgID uuid.UUID `json:"org_id"`
}

// TaskHandler consumes export:run tasks on the I/O queue.
type TaskHandler struct {
	repo      Store
	store     storage.Backend
	sender    email.Sender
	logger    *slog.Logger
	batchSize int
	linkTTL   time.Duration
}

// NewTaskHandler builds the export worker. sender may be nil; when nil the
// export still completes but no completion email is sent.
func NewTaskHandler(repo Store, store storage.Backend, sender email.Sender, batchSize int, linkTTL time.Duration, logger *slog.Logger) *TaskHandler {
	if logger == nil {
		logger = slog.Default()
	}
	if batchSize <= 0 || batchSize > MaxBatchSize {
		batchSize = 1000
	}
	if linkTTL <= 0 {
		linkTTL = 24 * time.Hour
	}
	return &TaskHandler{
		repo:      repo,
		store:     store,
		sender:    sender,
		logger:    logger,
		batchSize: batchSize,
		linkTTL:   linkTTL,
	}
}

// Run builds one CSV export and records the result.
func (h *TaskHandler) Run(ctx context.Context, task *asynq.Task) error {
	var p TaskPayload
	if err := json.Unmarshal(task.Payload(), &p); err != nil {
		return queue.Permanent(fmt.Errorf("decode export task: %w", err))
	}
	if p.JobID == uuid.Nil || p.OrgID == uuid.Nil {
		return queue.Permanent(fmt.Errorf("export task payload is incomplete"))
	}

	job, ok, err := h.repo.MarkRunning(ctx, p.OrgID, p.JobID)
	if err != nil {
		return err
	}
	if !ok {
		h.logger.Debug("export already finished, skipping", "export_id", p.JobID)
		return nil
	}

	var filter Filter
	if err := json.Unmarshal(job.Filters, &filter); err != nil {
		_ = h.repo.MarkFailed(ctx, job.OrgID, job.ID, "invalid stored filter")
		return queue.Permanent(fmt.Errorf("decode stored export filter: %w", err))
	}

	key := ExportKey(job.OrgID, job.ID)
	rows, err := h.writeCSV(ctx, job.OrgID, key, filter)
	if err != nil {
		_ = h.repo.MarkFailed(ctx, job.OrgID, job.ID, err.Error())
		return fmt.Errorf("build export %s: %w", job.ID, err)
	}

	if err := h.repo.MarkDone(ctx, job.OrgID, job.ID, key, rows); err != nil {
		return err
	}
	h.logger.Info("export completed", "export_id", job.ID, "rows", rows, "bytes_key", key)

	h.notify(ctx, job, rows, key)
	return nil
}

// writeCSV streams the whole export through an io.Pipe into object storage.
// The writer goroutine reads one keyset page at a time, so at most batchSize
// rows are resident no matter how large the export is.
func (h *TaskHandler) writeCSV(ctx context.Context, orgID uuid.UUID, key string, filter Filter) (int64, error) {
	pr, pw := io.Pipe()

	type result struct {
		rows int64
		err  error
	}
	done := make(chan result, 1)
	go func() {
		rows, err := h.stream(ctx, orgID, filter, pw)
		if err != nil {
			_ = pw.CloseWithError(err)
		} else {
			_ = pw.Close()
		}
		done <- result{rows: rows, err: err}
	}()

	putErr := h.store.Put(ctx, key, pr, -1, "text/csv")
	if putErr != nil {
		// Unblock the writer if storage rejected the upload.
		_ = pr.CloseWithError(putErr)
	}
	res := <-done

	if putErr != nil {
		return 0, putErr
	}
	if res.err != nil {
		return 0, res.err
	}
	return res.rows, nil
}

func (h *TaskHandler) stream(ctx context.Context, orgID uuid.UUID, filter Filter, w io.Writer) (int64, error) {
	cw := csv.NewWriter(w)
	if err := cw.Write(CSVHeader); err != nil {
		return 0, fmt.Errorf("write csv header: %w", err)
	}

	var (
		count   int64
		after   time.Time
		afterID uuid.UUID
	)
	for {
		if err := ctx.Err(); err != nil {
			return count, err
		}

		rows, err := h.repo.ListTicketRows(ctx, orgID, filter, after, afterID, h.batchSize)
		if err != nil {
			return count, err
		}
		if len(rows) == 0 {
			break
		}
		for _, row := range rows {
			if err := cw.Write(CSVRecord(row)); err != nil {
				return count, fmt.Errorf("write csv row: %w", err)
			}
			count++
		}

		last := rows[len(rows)-1]
		after, afterID = last.CreatedAt, last.ID
		if len(rows) < h.batchSize {
			break
		}
	}

	cw.Flush()
	if err := cw.Error(); err != nil {
		return count, fmt.Errorf("flush csv: %w", err)
	}
	return count, nil
}

// notify emails a 24-hour signed link to the requester. Notification failure
// never fails the export: the job is already done and the link can be fetched
// again through GET /v1/exports/{id}.
func (h *TaskHandler) notify(ctx context.Context, job Job, rows int64, key string) {
	if h.sender == nil {
		return
	}
	to, orgName, err := h.repo.LoadRecipient(ctx, job.OrgID, job.RequestedBy)
	if err != nil {
		h.logger.Warn("export completion email skipped", "export_id", job.ID, "error", err)
		return
	}
	disposition := fmt.Sprintf(`attachment; filename="tickets-%s.csv"`, job.ID)
	url, err := h.store.PresignGet(ctx, key, h.linkTTL, disposition)
	if err != nil {
		h.logger.Warn("export completion link failed", "export_id", job.ID, "error", err)
		return
	}

	payload := email.DeliveryPayload{
		OrgName:    orgName,
		Subject:    "Your ticket export is ready",
		ExportURL:  url,
		ExportRows: rows,
	}
	if err := h.sender.Send(ctx, to, email.TemplateExportReady, payload); err != nil {
		h.logger.Warn("export completion email failed", "export_id", job.ID, "error", err)
	}
}
