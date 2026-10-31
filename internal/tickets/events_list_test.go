package tickets

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func TestListEvents_TimelineInOrder(t *testing.T) {
	h, repo := newTestHandler(t)
	orgA, _, number := setupTwoOrgs(t, repo)
	agentID := seedMembership(t, repo, orgA)
	ctx := context.Background()

	// assign (ticket.assigned)
	if _, err := repo.AssignTicket(ctx, AssignInput{
		OrgID: orgA, Number: number,
		Actor:      Actor{Type: "user", ID: &agentID},
		AssigneeID: &agentID,
	}); err != nil {
		t.Fatalf("assign: %v", err)
	}

	// tag (ticket.tagged)
	if _, err := repo.CreateTag(ctx, orgA, "billing"); err != nil {
		t.Fatalf("tag: %v", err)
	}
	if err := repo.AttachTag(ctx, orgA, number, "billing", Actor{Type: "user", ID: &agentID}); err != nil {
		t.Fatalf("attach: %v", err)
	}

	// comment (comment.added)
	if _, err := repo.CreateComment(ctx, CreateCommentInput{
		OrgID: orgA, Number: number,
		Actor: Actor{Type: "user", ID: &agentID},
		Body:  "looking",
	}); err != nil {
		t.Fatalf("comment: %v", err)
	}

	// status change (ticket.status_changed)
	open := "open"
	if _, err := repo.UpdateTicket(ctx, UpdateInput{
		OrgID: orgA, Number: number, ExpectedVersion: 2,
		Actor: Actor{Type: "user", ID: &agentID}, Status: &open,
	}); err != nil {
		t.Fatalf("update: %v", err)
	}

	r := chi.NewRouter()
	r.Get("/tickets/{number}/events", h.ListEvents)

	req := httptest.NewRequest("GET",
		"/tickets/"+strconv.FormatInt(number, 10)+"/events", nil)
	req = req.WithContext(principalCtx(orgA, uuid.New()))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp TicketEventListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Events) < 5 {
		t.Fatalf("expected at least 5 events, got %d", len(resp.Events))
	}

	// order: created, assigned, tagged, comment.added, status_changed
	wantTypes := []string{
		"ticket.created",
		"ticket.assigned",
		"ticket.tagged",
		"comment.added",
		"ticket.status_changed",
	}
	for i, want := range wantTypes {
		if resp.Events[i].EventType != want {
			t.Fatalf("event %d: expected %q, got %q", i, want, resp.Events[i].EventType)
		}
	}
}

func TestListEvents_Pagination(t *testing.T) {
	h, repo := newTestHandler(t)
	orgA, _, number := setupTwoOrgs(t, repo)
	agentID := seedMembership(t, repo, orgA)
	ctx := context.Background()

	// create 5 comments (plus the initial ticket.created = 6 events)
	for i := 0; i < 5; i++ {
		if _, err := repo.CreateComment(ctx, CreateCommentInput{
			OrgID: orgA, Number: number,
			Actor: Actor{Type: "user", ID: &agentID},
			Body:  "c",
		}); err != nil {
			t.Fatalf("comment: %v", err)
		}
	}

	r := chi.NewRouter()
	r.Get("/tickets/{number}/events", h.ListEvents)

	seen := map[string]bool{}
	cursor := ""
	pages := 0
	for {
		url := "/tickets/" + strconv.FormatInt(number, 10) + "/events?limit=2"
		if cursor != "" {
			url += "&cursor=" + cursor
		}
		req := httptest.NewRequest("GET", url, nil)
		req = req.WithContext(principalCtx(orgA, uuid.New()))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("page: expected 200, got %d", rec.Code)
		}
		var resp TicketEventListResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		for _, e := range resp.Events {
			if seen[e.ID] {
				t.Fatalf("duplicate event %s", e.ID)
			}
			seen[e.ID] = true
		}
		pages++
		if resp.NextCursor == "" {
			break
		}
		cursor = resp.NextCursor
		if pages > 10 {
			t.Fatal("cursor loop")
		}
	}

	if len(seen) != 6 {
		t.Fatalf("expected 6 unique events, got %d", len(seen))
	}
}

func TestListEvents_OtherOrg_404(t *testing.T) {
	h, repo := newTestHandler(t)
	_, orgB, number := setupTwoOrgs(t, repo)

	r := chi.NewRouter()
	r.Get("/tickets/{number}/events", h.ListEvents)

	req := httptest.NewRequest("GET",
		"/tickets/"+strconv.FormatInt(number, 10)+"/events", nil)
	req = req.WithContext(principalCtx(orgB, uuid.New()))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}
