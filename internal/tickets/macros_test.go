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

func TestCreateMacro_HappyPath(t *testing.T) {
	h, repo := newTestHandler(t)
	orgA, _, _ := setupTwoOrgs(t, repo)
	creatorID := seedMembership(t, repo, orgA)

	r := chi.NewRouter()
	r.Post("/macros", h.CreateMacro)

	body, _ := json.Marshal(CreateMacroRequest{
		Name: "Mark pending",
		Actions: MacroActionsDTO{
			SetStatus: "pending",
			AddTags:   []string{"billing"},
			Comment:   "Waiting on the customer.",
		},
	})
	req := httptest.NewRequest("POST", "/macros", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(principalCtx(orgA, creatorID))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCreateMacro_InvalidStatus_422(t *testing.T) {
	h, repo := newTestHandler(t)
	orgA, _, _ := setupTwoOrgs(t, repo)
	creatorID := seedMembership(t, repo, orgA)

	r := chi.NewRouter()
	r.Post("/macros", h.CreateMacro)

	body, _ := json.Marshal(CreateMacroRequest{
		Name:    "bad",
		Actions: MacroActionsDTO{SetStatus: "not-a-status"},
	})
	req := httptest.NewRequest("POST", "/macros", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(principalCtx(orgA, creatorID))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d", rec.Code)
	}
}

func TestCreateMacro_Duplicate_409(t *testing.T) {
	h, repo := newTestHandler(t)
	orgA, _, _ := setupTwoOrgs(t, repo)
	creatorID := seedMembership(t, repo, orgA)

	r := chi.NewRouter()
	r.Post("/macros", h.CreateMacro)

	body, _ := json.Marshal(CreateMacroRequest{
		Name: "dup", Actions: MacroActionsDTO{SetStatus: "open"},
	})
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("POST", "/macros", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(principalCtx(orgA, creatorID))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if i == 1 && rec.Code != http.StatusConflict {
			t.Fatalf("expected 409 on second create, got %d: %s", rec.Code, rec.Body.String())
		}
	}
}

func TestApplyMacro_StatusAndTagAndComment(t *testing.T) {
	h, repo := newTestHandler(t)
	orgA, _, number := setupTwoOrgs(t, repo)
	creatorID := seedMembership(t, repo, orgA)
	ctx := context.Background()

	if _, err := repo.CreateTag(ctx, orgA, "billing"); err != nil {
		t.Fatalf("tag: %v", err)
	}

	m, err := repo.CreateMacro(ctx, orgA, creatorID, "work", []byte(
		`{"set_status":"open","add_tags":["billing"],"comment":"on it"}`))
	if err != nil {
		t.Fatalf("macro: %v", err)
	}

	r := chi.NewRouter()
	r.Post("/tickets/{number}/macros/{id}", h.ApplyMacro)

	req := httptest.NewRequest("POST",
		"/tickets/"+strconv.FormatInt(number, 10)+"/macros/"+m.ID.String(), nil)
	req = req.WithContext(principalCtx(orgA, creatorID))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}

	var status string
	_ = repo.pool.QueryRow(ctx, `SELECT status FROM tickets WHERE org_id = $1 AND number = $2`,
		orgA, number).Scan(&status)
	if status != "open" {
		t.Fatalf("expected status open, got %s", status)
	}

	var tagged, comments int
	_ = repo.pool.QueryRow(ctx,
		`SELECT count(*) FROM ticket_tags tt JOIN tickets t ON t.id = tt.ticket_id
		 WHERE t.org_id = $1 AND t.number = $2`, orgA, number).Scan(&tagged)
	if tagged != 1 {
		t.Fatalf("expected 1 tag, got %d", tagged)
	}
	_ = repo.pool.QueryRow(ctx,
		`SELECT count(*) FROM comments WHERE org_id = $1 AND ticket_id = (SELECT id FROM tickets WHERE org_id = $1 AND number = $2)`,
		orgA, number).Scan(&comments)
	if comments < 2 {
		t.Fatalf("expected initial + macro comment, got %d", comments)
	}
}

func TestApplyMacro_IllegalTransition_422(t *testing.T) {
	h, repo := newTestHandler(t)
	orgA, _, number := setupTwoOrgs(t, repo)
	creatorID := seedMembership(t, repo, orgA)
	ctx := context.Background()

	m, err := repo.CreateMacro(ctx, orgA, creatorID, "close", []byte(`{"set_status":"closed"}`))
	if err != nil {
		t.Fatalf("macro: %v", err)
	}

	r := chi.NewRouter()
	r.Post("/tickets/{number}/macros/{id}", h.ApplyMacro)

	req := httptest.NewRequest("POST",
		"/tickets/"+strconv.FormatInt(number, 10)+"/macros/"+m.ID.String(), nil)
	req = req.WithContext(principalCtx(orgA, creatorID))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for new->closed, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestApplyMacro_OtherOrgMacro_404(t *testing.T) {
	h, repo := newTestHandler(t)
	orgA, orgB, number := setupTwoOrgs(t, repo)
	creatorB := seedMembership(t, repo, orgB)
	ctx := context.Background()

	m, err := repo.CreateMacro(ctx, orgB, creatorB, "other", []byte(`{"set_status":"open"}`))
	if err != nil {
		t.Fatalf("macro: %v", err)
	}

	r := chi.NewRouter()
	r.Post("/tickets/{number}/macros/{id}", h.ApplyMacro)

	req := httptest.NewRequest("POST",
		"/tickets/"+strconv.FormatInt(number, 10)+"/macros/"+m.ID.String(), nil)
	req = req.WithContext(principalCtx(orgA, uuid.New()))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}
