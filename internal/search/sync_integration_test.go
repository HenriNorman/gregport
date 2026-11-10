package search

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

// getIndexedDoc fetches one document through the alias and returns its subject
// and version. A missing document returns found=false.
func getIndexedDoc(t *testing.T, client *Client, id uuid.UUID) (subject string, version int64, found bool) {
	t.Helper()
	res, err := client.ES().Get(
		client.Alias(),
		id.String(),
		client.ES().Get.WithContext(context.Background()),
	)
	if err != nil {
		t.Fatalf("get document: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode == 404 {
		return "", 0, false
	}
	if err := ResponseError(res); err != nil {
		t.Fatalf("get document: %v", err)
	}
	var payload struct {
		Version int64 `json:"_version"`
		Source  struct {
			Subject string `json:"subject"`
		} `json:"_source"`
	}
	if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
		t.Fatalf("decode get document: %v", err)
	}
	return payload.Source.Subject, payload.Version, true
}

func TestESDocumentIndexerIntegration(t *testing.T) {
	client := integrationESClient(t)
	manager := NewIndexManager(client)
	ctx := context.Background()
	if err := manager.Bootstrap(ctx); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	target, err := manager.AliasTarget(ctx)
	if err != nil {
		t.Fatalf("alias target: %v", err)
	}
	t.Cleanup(func() { _ = manager.DeleteIndex(context.Background(), target) })

	indexer := NewESDocumentIndexer(client)
	orgID := uuid.New()
	otherOrgID := uuid.New()
	id := uuid.New()
	base := time.Date(2026, 2, 1, 9, 0, 0, 0, time.UTC)

	doc := func(version int64, subject string) TicketDocument {
		return TicketDocument{
			ID: id, OrgID: orgID, Number: 1, Subject: subject,
			CommentsText: "the printer jams", Status: "open", Priority: "high",
			Tags: []string{"hardware"}, RequesterEmail: "h@acme.com",
			CreatedAt: base, UpdatedAt: base, Version: version,
		}
	}

	if err := indexer.IndexTicket(ctx, doc(2, "second version")); err != nil {
		t.Fatalf("index v2: %v", err)
	}
	subject, _, found := getIndexedDoc(t, client, id)
	if !found || subject != "second version" {
		t.Fatalf("doc after v2 = %q found=%v", subject, found)
	}

	// A delayed job carrying an older version must not overwrite the newer
	// document. Version conflicts are success for derived data.
	if err := indexer.IndexTicket(ctx, doc(1, "first version")); err != nil {
		t.Fatalf("index stale v1: %v", err)
	}
	subject, version, found := getIndexedDoc(t, client, id)
	if !found || subject != "second version" || version != 2 {
		t.Fatalf("doc after stale v1 = %q version=%d found=%v", subject, version, found)
	}

	// A newer version replaces the document.
	if err := indexer.IndexTicket(ctx, doc(3, "third version")); err != nil {
		t.Fatalf("index v3: %v", err)
	}
	subject, version, found = getIndexedDoc(t, client, id)
	if !found || subject != "third version" || version != 3 {
		t.Fatalf("doc after v3 = %q version=%d found=%v", subject, version, found)
	}

	// Deleting with the wrong org must be a no-op, even for a real ticket id.
	if err := indexer.DeleteTicket(ctx, otherOrgID, id, 4); err != nil {
		t.Fatalf("delete with foreign org: %v", err)
	}
	if _, _, found := getIndexedDoc(t, client, id); !found {
		t.Fatal("foreign-org delete removed the document")
	}

	// The correct org deletes the document.
	if err := indexer.DeleteTicket(ctx, orgID, id, 4); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, _, found := getIndexedDoc(t, client, id); found {
		t.Fatal("document still exists after delete")
	}

	// Deleting an already-missing document is success, so a replayed job
	// converges instead of retrying forever.
	if err := indexer.DeleteTicket(ctx, orgID, id, 4); err != nil {
		t.Fatalf("delete missing doc: %v", err)
	}
}

func TestESDocumentIndexerRejectsInvalidDocuments(t *testing.T) {
	client := integrationESClient(t)
	indexer := NewESDocumentIndexer(client)
	ctx := context.Background()

	if err := indexer.IndexTicket(ctx, TicketDocument{ID: uuid.New(), OrgID: uuid.Nil, Version: 1}); !errors.Is(err, ErrMissingOrg) {
		t.Fatalf("missing org error = %v, want ErrMissingOrg", err)
	}
	if err := indexer.IndexTicket(ctx, TicketDocument{ID: uuid.New(), OrgID: uuid.New(), Version: 0}); err == nil {
		t.Fatal("zero version = nil error")
	}
	if err := indexer.DeleteTicket(ctx, uuid.Nil, uuid.New(), 0); !errors.Is(err, ErrMissingOrg) {
		t.Fatalf("delete missing org error = %v, want ErrMissingOrg", err)
	}
}
