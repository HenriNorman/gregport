package tickets

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func TestMerge_HappyPath(t *testing.T) {
	h, repo := newTestHandler(t)
	orgA, _, sourceNum := setupTwoOrgs(t, repo)

	// create a second ticket with the same requester
	target, _, err := repo.CreateTicket(context.Background(), CreateInput{
		OrgID: orgA, Actor: Actor{Type: "system"},
		RequesterEmail: "req@test.local", RequesterName: "Req",
		Subject: "duplicate", Body: "b", Priority: "normal",
	})
	if err != nil {
		t.Fatalf("create target: %v", err)
	}

	r := chi.NewRouter()
	r.Post("/tickets/{number}/merge", h.Merge)

	body, _ := json.Marshal(MergeTicketRequest{IntoNumber: target.Number})
	req := httptest.NewRequest("POST",
		"/tickets/"+strconv.FormatInt(sourceNum, 10)+"/merge", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(principalCtx(orgA, uuid.New()))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var count int
	_ = repo.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM ticket_events WHERE event_type = 'ticket.merged'`).Scan(&count)
	if count != 1 {
		t.Fatalf("expected 1 ticket.merged event, got %d", count)
	}

	// source is closed with merged_into_id set
	var status string
	var mergedInto *uuid.UUID
	_ = repo.pool.QueryRow(context.Background(),
		`SELECT status, merged_into_id FROM tickets WHERE org_id = $1 AND number = $2`,
		orgA, sourceNum).Scan(&status, &mergedInto)
	if status != "closed" || mergedInto == nil {
		t.Fatalf("source not closed/merged: status=%s merged=%v", status, mergedInto)
	}
}

func TestMerge_Self_422(t *testing.T) {
	h, repo := newTestHandler(t)
	orgA, _, sourceNum := setupTwoOrgs(t, repo)

	r := chi.NewRouter()
	r.Post("/tickets/{number}/merge", h.Merge)

	body, _ := json.Marshal(MergeTicketRequest{IntoNumber: sourceNum})
	req := httptest.NewRequest("POST",
		"/tickets/"+strconv.FormatInt(sourceNum, 10)+"/merge", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(principalCtx(orgA, uuid.New()))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d", rec.Code)
	}
}

func TestMerge_OtherOrg_404(t *testing.T) {
	h, repo := newTestHandler(t)
	orgA, orgB, sourceNum := setupTwoOrgs(t, repo)

	// bump org B's counter so its ticket number differs from org A's
	if _, _, err := repo.CreateTicket(context.Background(), CreateInput{
		OrgID: orgB, Actor: Actor{Type: "system"},
		RequesterEmail: "req@test.local", RequesterName: "Req",
		Subject: "bump", Body: "b", Priority: "normal",
	}); err != nil {
		t.Fatalf("bump: %v", err)
	}

	other, _, err := repo.CreateTicket(context.Background(), CreateInput{
		OrgID: orgB, Actor: Actor{Type: "system"},
		RequesterEmail: "req@test.local", RequesterName: "Req",
		Subject: "other", Body: "b", Priority: "normal",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	r := chi.NewRouter()
	r.Post("/tickets/{number}/merge", h.Merge)

	body, _ := json.Marshal(MergeTicketRequest{IntoNumber: other.Number})
	req := httptest.NewRequest("POST",
		"/tickets/"+strconv.FormatInt(sourceNum, 10)+"/merge", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(principalCtx(orgA, uuid.New()))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestMerge_Concurrent_OneSuccess(t *testing.T) {
	h, repo := newTestHandler(t)
	orgA, _, sourceNum := setupTwoOrgs(t, repo)

	target, _, err := repo.CreateTicket(context.Background(), CreateInput{
		OrgID: orgA, Actor: Actor{Type: "system"},
		RequesterEmail: "req@test.local", RequesterName: "Req",
		Subject: "target", Body: "b", Priority: "normal",
	})
	if err != nil {
		t.Fatalf("create target: %v", err)
	}

	r := chi.NewRouter()
	r.Post("/tickets/{number}/merge", h.Merge)

	body, _ := json.Marshal(MergeTicketRequest{IntoNumber: target.Number})

	var wg sync.WaitGroup
	codes := make([]int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := httptest.NewRequest("POST",
				"/tickets/"+strconv.FormatInt(sourceNum, 10)+"/merge",
				bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req = req.WithContext(principalCtx(orgA, uuid.New()))
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			codes[i] = rec.Code
		}(i)
	}
	wg.Wait()

	var ok, conflict int
	for _, c := range codes {
		switch c {
		case http.StatusOK:
			ok++
		case http.StatusUnprocessableEntity, http.StatusConflict:
			conflict++
		default:
			t.Fatalf("unexpected status: %d", c)
		}
	}
	if ok != 1 || conflict != 1 {
		t.Fatalf("expected exactly one success and one failure, got ok=%d conflict=%d", ok, conflict)
	}
}

func TestMerge_SourceReadOnly_409(t *testing.T) {
	h, repo := newTestHandler(t)
	orgA, _, sourceNum := setupTwoOrgs(t, repo)

	target, _, err := repo.CreateTicket(context.Background(), CreateInput{
		OrgID: orgA, Actor: Actor{Type: "system"},
		RequesterEmail: "req@test.local", RequesterName: "Req",
		Subject: "target", Body: "b", Priority: "normal",
	})
	if err != nil {
		t.Fatalf("create target: %v", err)
	}

	if _, _, err := repo.MergeTickets(context.Background(), orgA, sourceNum, target.Number,
		Actor{Type: "system"}); err != nil {
		t.Fatalf("merge: %v", err)
	}

	r := chi.NewRouter()
	r.Post("/tickets/{number}/comments", h.CreateComment)

	body, _ := json.Marshal(CreateCommentRequest{Body: "should fail"})
	req := httptest.NewRequest("POST",
		"/tickets/"+strconv.FormatInt(sourceNum, 10)+"/comments", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(principalCtx(orgA, uuid.New()))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 on merged source, got %d: %s", rec.Code, rec.Body.String())
	}
}
