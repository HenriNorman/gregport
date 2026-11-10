package search

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/elastic/go-elasticsearch/v8/esapi"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"

	"github.com/HenriNorman/gregport/internal/queue"
)

// DocumentIndexer writes and removes derived documents.
type DocumentIndexer interface {
	IndexTicket(ctx context.Context, doc TicketDocument) error
	DeleteTicket(ctx context.Context, orgID, ticketID uuid.UUID, version int64) error
}

// ESDocumentIndexer writes documents through the stable alias. Every write
// carries the ticket's external version, so an out-of-order job can never
// overwrite a newer document.
type ESDocumentIndexer struct {
	client *Client
}

// NewESDocumentIndexer creates the Elasticsearch document writer.
func NewESDocumentIndexer(client *Client) *ESDocumentIndexer {
	return &ESDocumentIndexer{client: client}
}

// IndexTicket indexes one ticket document. A version conflict means a newer
// document is already present, which is success: the index is derived data.
func (i *ESDocumentIndexer) IndexTicket(ctx context.Context, doc TicketDocument) error {
	if doc.ID == uuid.Nil || doc.OrgID == uuid.Nil {
		return ErrMissingOrg
	}
	if doc.Version <= 0 {
		return fmt.Errorf("search: index document %s has invalid version %d", doc.ID, doc.Version)
	}
	body, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("search: marshal index document: %w", err)
	}

	res, err := i.client.ES().Index(
		i.client.Alias(),
		bytes.NewReader(body),
		i.client.ES().Index.WithContext(ctx),
		i.client.ES().Index.WithDocumentID(doc.ID.String()),
		i.client.ES().Index.WithVersion(int(doc.Version)),
		i.client.ES().Index.WithVersionType("external"),
	)
	if err != nil {
		return fmt.Errorf("%w: index ticket: %v", ErrSearchUnavailable, err)
	}
	defer res.Body.Close()
	if err := ResponseError(res); err != nil {
		if errors.Is(err, ErrVersionConflict) {
			return nil
		}
		return err
	}
	return nil
}

// DeleteTicket removes one document. It verifies the stored org id first so a
// malformed event payload cannot delete another tenant's document; the ticket
// id is global, but defense in depth is cheap on the rare delete path.
func (i *ESDocumentIndexer) DeleteTicket(ctx context.Context, orgID, ticketID uuid.UUID, version int64) error {
	if orgID == uuid.Nil || ticketID == uuid.Nil {
		return ErrMissingOrg
	}

	getRes, err := i.client.ES().Get(
		i.client.Alias(),
		ticketID.String(),
		i.client.ES().Get.WithContext(ctx),
	)
	if err != nil {
		return fmt.Errorf("%w: get ticket for delete: %v", ErrSearchUnavailable, err)
	}
	defer getRes.Body.Close()
	if getRes.StatusCode == 404 {
		return nil
	}
	if err := ResponseError(getRes); err != nil {
		if errors.Is(err, ErrIndexNotFound) {
			return nil
		}
		return err
	}

	var current struct {
		Source struct {
			OrgID string `json:"org_id"`
		} `json:"_source"`
		Version int64 `json:"_version"`
	}
	if err := json.NewDecoder(getRes.Body).Decode(&current); err != nil {
		return fmt.Errorf("search: decode ticket before delete: %w", err)
	}
	if current.Source.OrgID != orgID.String() {
		// Never delete a document belonging to another org.
		return nil
	}

	opts := []func(*esapi.DeleteRequest){
		i.client.ES().Delete.WithContext(ctx),
		i.client.ES().Delete.WithVersionType("external"),
	}
	if version > 0 {
		// External versions must increase. The DB version is already ahead
		// of an active document after a soft delete, but guard against a
		// replay that arrives after another delete.
		deleteVersion := version
		if deleteVersion <= current.Version {
			deleteVersion = current.Version + 1
		}
		opts = append(opts, i.client.ES().Delete.WithVersion(int(deleteVersion)))
	}

	res, err := i.client.ES().Delete(i.client.Alias(), ticketID.String(), opts...)
	if err != nil {
		return fmt.Errorf("%w: delete ticket document: %v", ErrSearchUnavailable, err)
	}
	defer res.Body.Close()
	if err := ResponseError(res); err != nil {
		if errors.Is(err, ErrVersionConflict) || errors.Is(err, ErrIndexNotFound) {
			// A newer version exists, or the document is already gone.
			return nil
		}
		return err
	}
	return nil
}

// SyncPayload identifies the ticket whose document should be refreshed.
type SyncPayload struct {
	OrgID    uuid.UUID `json:"org_id"`
	TicketID uuid.UUID `json:"ticket_id"`
}

// NewSyncTask builds the queue task for one ticket.
func NewSyncTask(orgID, ticketID uuid.UUID) (*asynq.Task, error) {
	payload, err := json.Marshal(SyncPayload{OrgID: orgID, TicketID: ticketID})
	if err != nil {
		return nil, fmt.Errorf("search: marshal sync payload: %w", err)
	}
	return asynq.NewTask(queue.TaskSearchIndex, payload), nil
}

// SyncHandler processes one ticket index job. It always loads the current
// Postgres row instead of trusting event data, which makes duplicate and
// out-of-order deliveries converge on the latest state.
type SyncHandler struct {
	loader  DocumentLoader
	indexer DocumentIndexer
	logger  *slog.Logger
}

// NewSyncHandler creates the index job handler.
func NewSyncHandler(loader DocumentLoader, indexer DocumentIndexer, logger *slog.Logger) *SyncHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &SyncHandler{loader: loader, indexer: indexer, logger: logger}
}

// Handle implements asynq.Handler.
func (h *SyncHandler) Handle(ctx context.Context, task *asynq.Task) error {
	var payload SyncPayload
	if err := json.Unmarshal(task.Payload(), &payload); err != nil {
		return fmt.Errorf("search: decode sync payload: %w", err)
	}
	if payload.OrgID == uuid.Nil || payload.TicketID == uuid.Nil {
		return errors.New("search: sync payload missing org or ticket id")
	}

	state, err := h.loader.LoadDocument(ctx, payload.OrgID, payload.TicketID)
	if err != nil {
		return err
	}
	if !state.Found {
		// The ticket was hard-purged or never existed in this org. Removing
		// the id is safe and heals an index left behind by a failed job.
		return h.indexer.DeleteTicket(ctx, payload.OrgID, payload.TicketID, 0)
	}
	if state.Deleted {
		return h.indexer.DeleteTicket(ctx, payload.OrgID, payload.TicketID, state.Version)
	}
	return h.indexer.IndexTicket(ctx, state.Document)
}

// Compile-time checks for the production implementations.
var _ DocumentIndexer = (*ESDocumentIndexer)(nil)
