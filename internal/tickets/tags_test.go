package tickets

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func TestCreateTag_HappyPath_Lowercased(t *testing.T) {
	h, repo := newTestHandler(t)
	orgA, _, _ := setupTwoOrgs(t, repo)

	r := chi.NewRouter()
	r.Post("/tags", h.CreateTag)

	body, _ := json.Marshal(CreateTagRequest{Name: "  Billing  "})
	req := httptest.NewRequest("POST", "/tags", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(principalCtx(orgA, uuid.New()))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var count int
	_ = repo.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM tags WHERE org_id = $1 AND name = 'billing'`, orgA).Scan(&count)
	if count != 1 {
		t.Fatalf("expected tag 'billing' (lowercased, trimmed), got %d", count)
	}
}

func TestCreateTag_Duplicate_409(t *testing.T) {
	h, repo := newTestHandler(t)
	orgA, _, _ := setupTwoOrgs(t, repo)

	r := chi.NewRouter()
	r.Post("/tags", h.CreateTag)

	body, _ := json.Marshal(CreateTagRequest{Name: "billing"})
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("POST", "/tags", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(principalCtx(orgA, uuid.New()))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if i == 1 && rec.Code != http.StatusConflict {
			t.Fatalf("expected 409 on second create, got %d", rec.Code)
		}
	}
}

func TestAttachTag_Idempotent_OneEvent(t *testing.T) {
	h, repo := newTestHandler(t)
	orgA, _, number := setupTwoOrgs(t, repo)

	if _, err := repo.CreateTag(context.Background(), orgA, "billing"); err != nil {
		t.Fatalf("tag: %v", err)
	}

	r := chi.NewRouter()
	r.Put("/tickets/{number}/tags/{tag}", h.AttachTag)

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("PUT",
			"/tickets/"+strconv.FormatInt(number, 10)+"/tags/billing", nil)
		req = req.WithContext(principalCtx(orgA, uuid.New()))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("attach %d: expected 204, got %d: %s", i, rec.Code, rec.Body.String())
		}
	}

	var events int
	_ = repo.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM ticket_events WHERE event_type = 'ticket.tagged'`).Scan(&events)
	if events != 1 {
		t.Fatalf("expected exactly 1 ticket.tagged event (idempotent), got %d", events)
	}
}

func TestDetachTag_Idempotent_OneEvent(t *testing.T) {
	h, repo := newTestHandler(t)
	orgA, _, number := setupTwoOrgs(t, repo)

	if _, err := repo.CreateTag(context.Background(), orgA, "billing"); err != nil {
		t.Fatalf("tag: %v", err)
	}
	if err := repo.AttachTag(context.Background(), orgA, number, "billing",
		Actor{Type: "system"}); err != nil {
		t.Fatalf("attach: %v", err)
	}

	r := chi.NewRouter()
	r.Delete("/tickets/{number}/tags/{tag}", h.DetachTag)

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("DELETE",
			"/tickets/"+strconv.FormatInt(number, 10)+"/tags/billing", nil)
		req = req.WithContext(principalCtx(orgA, uuid.New()))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("detach %d: expected 204, got %d: %s", i, rec.Code, rec.Body.String())
		}
	}

	var events int
	_ = repo.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM ticket_events WHERE event_type = 'ticket.untagged'`).Scan(&events)
	if events != 1 {
		t.Fatalf("expected exactly 1 ticket.untagged event (idempotent), got %d", events)
	}
}

func TestAttachTag_NonexistentTag_404(t *testing.T) {
	h, repo := newTestHandler(t)
	orgA, _, number := setupTwoOrgs(t, repo)

	r := chi.NewRouter()
	r.Put("/tickets/{number}/tags/{tag}", h.AttachTag)

	req := httptest.NewRequest("PUT",
		"/tickets/"+strconv.FormatInt(number, 10)+"/tags/never-created", nil)
	req = req.WithContext(principalCtx(orgA, uuid.New()))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing tag, got %d", rec.Code)
	}
}

func TestAttachTag_OtherOrgTicket_404(t *testing.T) {
	h, repo := newTestHandler(t)
	_, orgB, number := setupTwoOrgs(t, repo)

	if _, err := repo.CreateTag(context.Background(), orgB, "billing"); err != nil {
		t.Fatalf("tag: %v", err)
	}

	r := chi.NewRouter()
	r.Put("/tickets/{number}/tags/{tag}", h.AttachTag)

	req := httptest.NewRequest("PUT",
		"/tickets/"+strconv.FormatInt(number, 10)+"/tags/billing", nil)
	req = req.WithContext(principalCtx(orgB, uuid.New()))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for other-org ticket, got %d", rec.Code)
	}
}
