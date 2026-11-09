package exports

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/HenriNorman/gregport/internal/queue"
	"github.com/HenriNorman/gregport/internal/storage"
)

// Enqueuer enqueues the worker task that builds an export.
type Enqueuer interface {
	EnqueueJob(ctx context.Context, job queue.Job) error
}

// Service creates exports and returns signed download links.
type Service struct {
	repo     Store
	store    storage.Backend
	enqueuer Enqueuer
	linkTTL  time.Duration
	now      func() time.Time
}

// NewService wires the export service.
func NewService(repo Store, store storage.Backend, linkTTL time.Duration) *Service {
	if linkTTL <= 0 {
		linkTTL = 24 * time.Hour
	}
	return &Service{repo: repo, store: store, linkTTL: linkTTL, now: time.Now}
}

// SetEnqueuer wires the queue used to start export jobs.
func (s *Service) SetEnqueuer(e Enqueuer) { s.enqueuer = e }

// Create validates the filter, persists a pending job and enqueues the worker
// task. A job is always scoped to the caller's org.
func (s *Service) Create(ctx context.Context, orgID, requestedBy uuid.UUID, req CreateRequest) (DTO, error) {
	filter, err := req.ToFilter()
	if err != nil {
		return DTO{}, err
	}
	raw, err := json.Marshal(filter)
	if err != nil {
		return DTO{}, fmt.Errorf("exports: marshal filter: %w", err)
	}

	job, err := s.repo.CreateJob(ctx, orgID, requestedBy, raw)
	if err != nil {
		return DTO{}, err
	}

	if s.enqueuer != nil {
		payload, err := json.Marshal(TaskPayload{JobID: job.ID, OrgID: job.OrgID})
		if err != nil {
			return DTO{}, fmt.Errorf("exports: marshal task: %w", err)
		}
		if err := s.enqueuer.EnqueueJob(ctx, queue.Job{
			ID:       queue.TaskExportRun + ":" + job.ID.String(),
			Type:     queue.TaskExportRun,
			Queue:    queue.QueueLow,
			Payload:  payload,
			MaxRetry: 5,
		}); err != nil {
			return DTO{}, fmt.Errorf("exports: enqueue export: %w", err)
		}
	}

	return AsDTO(job, "", nil), nil
}

// Get returns one export scoped to the caller's org. A finished export carries
// a short-lived signed URL to the CSV object.
func (s *Service) Get(ctx context.Context, orgID, id uuid.UUID) (DTO, error) {
	job, err := s.repo.GetJob(ctx, orgID, id)
	if err != nil {
		return DTO{}, err
	}

	url := ""
	var expiresAt *time.Time
	if job.Status == StatusDone && job.StorageKey != "" {
		disposition := fmt.Sprintf(`attachment; filename="tickets-%s.csv"`, job.ID)
		url, err = s.store.PresignGet(ctx, job.StorageKey, s.linkTTL, disposition)
		if err != nil {
			return DTO{}, err
		}
		expiry := s.now().UTC().Add(s.linkTTL)
		expiresAt = &expiry
	}

	return AsDTO(job, url, expiresAt), nil
}
