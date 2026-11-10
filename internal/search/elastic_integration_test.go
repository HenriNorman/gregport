package search

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/HenriNorman/gregport/internal/platform/requestctx"
)

type esTicketDoc struct {
	OrgID          string    `json:"org_id"`
	Number         int64     `json:"number"`
	Subject        string    `json:"subject"`
	CommentsText   string    `json:"comments_text"`
	Status         string    `json:"status"`
	Priority       string    `json:"priority"`
	Tags           []string  `json:"tags"`
	RequesterEmail string    `json:"requester_email"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	Version        int64     `json:"version"`
}

func TestElasticBackendIntegration(t *testing.T) {
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

	orgA := uuid.New()
	orgB := uuid.New()
	base := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)

	docs := []struct {
		id    uuid.UUID
		doc   esTicketDoc
		canon TicketHit
	}{
		{
			id: uuid.New(),
			doc: esTicketDoc{
				OrgID: orgA.String(), Number: 3, Subject: "Printer failure on floor two",
				CommentsText: "The paper tray is jammed and the roller squeaks",
				Status:       "open", Priority: "high", Tags: []string{"hardware"},
				RequesterEmail: "a@example.com", CreatedAt: base, UpdatedAt: base, Version: 1,
			},
		},
		{
			id: uuid.New(),
			doc: esTicketDoc{
				OrgID: orgA.String(), Number: 2, Subject: "Network outage in office",
				CommentsText: "The router is down",
				Status:       "pending", Priority: "normal",
				RequesterEmail: "a@example.com", CreatedAt: base.Add(-time.Minute), UpdatedAt: base.Add(-time.Minute), Version: 1,
			},
		},
		{
			id: uuid.New(),
			doc: esTicketDoc{
				OrgID: orgB.String(), Number: 1, Subject: "Printer failure elsewhere",
				CommentsText: "same words, different tenant",
				Status:       "open", Priority: "normal",
				RequesterEmail: "b@example.com", CreatedAt: base, UpdatedAt: base, Version: 1,
			},
		},
	}

	audit := &fakeTicketStore{rows: map[uuid.UUID]TicketHit{}}
	for _, d := range docs {
		body, err := json.Marshal(d.doc)
		if err != nil {
			t.Fatalf("marshal doc: %v", err)
		}
		res, err := client.ES().Index(
			client.Alias(),
			bytes.NewReader(body),
			client.ES().Index.WithContext(ctx),
			client.ES().Index.WithDocumentID(d.id.String()),
		)
		if err != nil {
			t.Fatalf("index doc: %v", err)
		}
		res.Body.Close()
		if res.IsError() {
			t.Fatalf("index doc: %s", res.String())
		}
		audit.rows[d.id] = TicketHit{
			ID: d.id, OrgID: uuid.MustParse(d.doc.OrgID), Number: d.doc.Number,
			Subject: d.doc.Subject, Status: d.doc.Status, Priority: d.doc.Priority,
			RequesterEmail: d.doc.RequesterEmail,
			CreatedAt:      d.doc.CreatedAt, UpdatedAt: d.doc.UpdatedAt,
		}
	}
	refresh, err := client.ES().Indices.Refresh(
		client.ES().Indices.Refresh.WithContext(ctx),
		client.ES().Indices.Refresh.WithIndex(client.Alias()),
	)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	refresh.Body.Close()

	backend := NewElasticBackend(client, audit)
	svc := NewService(backend)
	pA := requestctx.Principal{Kind: requestctx.KindUser, OrgID: orgA, Role: "agent"}

	t.Run("org scoped subject search", func(t *testing.T) {
		res, err := svc.Search(ctx, pA, Filter{Query: "printer failure", Limit: 25})
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if len(res.Hits) != 1 || res.Hits[0].ID != docs[0].id {
			t.Fatalf("hits = %+v, want only the org A printer ticket", res.Hits)
		}
		if !strings.Contains(res.Hits[0].SubjectHighlight, "<mark>") {
			t.Fatalf("subject highlight = %q", res.Hits[0].SubjectHighlight)
		}
	})

	t.Run("comment search is highlighted and scoped", func(t *testing.T) {
		res, err := svc.Search(ctx, pA, Filter{Query: "paper tray", Limit: 25})
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if len(res.Hits) != 1 || res.Hits[0].ID != docs[0].id {
			t.Fatalf("hits = %+v", res.Hits)
		}
		if !strings.Contains(res.Hits[0].CommentHighlight, "<mark>") {
			t.Fatalf("comment highlight = %q", res.Hits[0].CommentHighlight)
		}
	})

	t.Run("org B never sees org A", func(t *testing.T) {
		pB := requestctx.Principal{Kind: requestctx.KindUser, OrgID: orgB, Role: "agent"}
		res, err := svc.Search(ctx, pB, Filter{Query: "printer failure", Limit: 25})
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if len(res.Hits) != 1 || res.Hits[0].ID != docs[2].id {
			t.Fatalf("org B hits = %+v", res.Hits)
		}
	})

	t.Run("pagination covers every org ticket once", func(t *testing.T) {
		seen := map[uuid.UUID]bool{}
		cursor := ""
		for page := 0; page < 5; page++ {
			res, err := svc.Search(ctx, pA, Filter{Limit: 1, Cursor: cursor})
			if err != nil {
				t.Fatalf("page %d: %v", page, err)
			}
			for _, h := range res.Hits {
				if seen[h.ID] {
					t.Fatalf("duplicate hit %s", h.ID)
				}
				seen[h.ID] = true
			}
			if res.NextCursor == "" {
				break
			}
			cursor = res.NextCursor
		}
		if len(seen) != 2 {
			t.Fatalf("pagination saw %d org A tickets, want 2", len(seen))
		}
	})
}
