package search

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

func TestDocumentRepositoryLoadsCanonicalDocument(t *testing.T) {
	if testing.Short() {
		t.Skip("postgres integration test")
	}
	pool := testPool(t)
	ctx := context.Background()
	repo := NewDocumentRepository(pool)

	orgID := seedOrg(t, pool)
	userID := seedUser(t, pool)
	custID := seedCustomer(t, pool, orgID)
	t.Cleanup(func() { cleanupOrg(t, pool, orgID, userID) })

	ticketID := seedTicket(t, pool, orgID, 7, custID, &userID, "Printer cannot print", "open")
	seedComment(t, pool, orgID, ticketID, "The paper tray is jammed")
	seedComment(t, pool, orgID, ticketID, "The roller squeaks")
	seedTag(t, pool, orgID, ticketID, "hardware")

	state, err := repo.LoadDocument(ctx, orgID, ticketID)
	if err != nil {
		t.Fatalf("LoadDocument: %v", err)
	}
	if !state.Found || state.Deleted {
		t.Fatalf("state = %+v, want active", state)
	}
	doc := state.Document
	if doc.ID != ticketID || doc.OrgID != orgID || doc.Number != 7 {
		t.Fatalf("doc identity = %+v", doc)
	}
	if doc.Subject != "Printer cannot print" || doc.Status != "open" {
		t.Fatalf("doc fields = %+v", doc)
	}
	if doc.AssigneeID == nil || *doc.AssigneeID != userID {
		t.Fatalf("assignee = %v", doc.AssigneeID)
	}
	if !strings.Contains(doc.RequesterEmail, "@test.local") {
		t.Fatalf("requester email = %q", doc.RequesterEmail)
	}
	if !strings.Contains(doc.CommentsText, "paper tray") || !strings.Contains(doc.CommentsText, "roller") {
		t.Fatalf("comments text = %q", doc.CommentsText)
	}
	if len(doc.Tags) != 1 || doc.Tags[0] != "hardware" {
		t.Fatalf("tags = %v", doc.Tags)
	}

	// Deletion keeps the version so the index job can delete safely.
	if _, err := pool.Exec(ctx,
		`UPDATE tickets SET deleted_at = now(), version = version + 1, updated_at = now() WHERE id = $1`,
		ticketID,
	); err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	state, err = repo.LoadDocument(ctx, orgID, ticketID)
	if err != nil {
		t.Fatalf("LoadDocument deleted: %v", err)
	}
	if !state.Found || !state.Deleted {
		t.Fatalf("deleted state = %+v", state)
	}
	if state.Version < 2 {
		t.Fatalf("deleted version = %d, want at least 2", state.Version)
	}

	// A ticket id from another org resolves to missing, not a row.
	otherOrg := uuid.New()
	state, err = repo.LoadDocument(ctx, otherOrg, ticketID)
	if err != nil {
		t.Fatalf("LoadDocument foreign org: %v", err)
	}
	if state.Found {
		t.Fatalf("foreign org state = %+v, want missing", state)
	}
}

func TestDocumentRepositoryCapsCommentsText(t *testing.T) {
	if testing.Short() {
		t.Skip("postgres integration test")
	}
	pool := testPool(t)
	ctx := context.Background()
	repo := NewDocumentRepository(pool)

	orgID := seedOrg(t, pool)
	userID := seedUser(t, pool)
	custID := seedCustomer(t, pool, orgID)
	t.Cleanup(func() { cleanupOrg(t, pool, orgID, userID) })
	ticketID := seedTicket(t, pool, orgID, 1, custID, nil, "Many comments", "open")

	for i := 0; i < 30; i++ {
		body := strings.Repeat("a", maxCommentBodyBytes)
		if i == 0 {
			// Ensure the final byte-boundary trim keeps valid UTF-8.
			body = strings.Repeat("é", maxCommentBodyBytes/2)
		}
		seedComment(t, pool, orgID, ticketID, body)
	}

	state, err := repo.LoadDocument(ctx, orgID, ticketID)
	if err != nil {
		t.Fatalf("LoadDocument: %v", err)
	}
	text := state.Document.CommentsText
	if len(text) > MaxCommentsIndexBytes {
		t.Fatalf("comments text = %d bytes, cap is %d", len(text), MaxCommentsIndexBytes)
	}
	if len(text) < MaxCommentsIndexBytes-100 {
		t.Fatalf("comments text = %d bytes, expected the cap to be filled", len(text))
	}
	if !utf8.ValidString(text) {
		t.Fatal("comments text is not valid UTF-8")
	}
}

func TestDocumentRepositoryRequiresIDs(t *testing.T) {
	repo := NewDocumentRepository(nil)
	if _, err := repo.LoadDocument(context.Background(), uuid.Nil, uuid.New()); err == nil {
		t.Fatal("LoadDocument without org = nil error")
	}
	if _, err := repo.LoadDocument(context.Background(), uuid.New(), uuid.Nil); err == nil {
		t.Fatal("LoadDocument without ticket = nil error")
	}
	_ = time.Second
}
