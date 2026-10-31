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

func TestTenant_AssignTicket_OtherOrg_404(t *testing.T) {
	h, repo := newTestHandler(t)
	_, orgB, number := setupTwoOrgs(t, repo)

	r := chi.NewRouter()
	r.Post("/tickets/{number}/assignment", h.Assign)

	body, _ := json.Marshal(AssignTicketRequest{AssigneeID: strPtr(uuid.New().String())})
	req := httptest.NewRequest("POST",
		"/tickets/"+strconv.FormatInt(number, 10)+"/assignment", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(principalCtx(orgB, uuid.New()))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestTenant_ListTags_Isolated(t *testing.T) {
	h, repo := newTestHandler(t)
	orgA, orgB, _ := setupTwoOrgs(t, repo)
	ctx := context.Background()

	if _, err := repo.CreateTag(ctx, orgA, "billing"); err != nil {
		t.Fatalf("create tag: %v", err)
	}

	r := chi.NewRouter()
	r.Get("/tags", h.ListTags)

	req := httptest.NewRequest("GET", "/tags", nil)
	req = req.WithContext(principalCtx(orgB, uuid.New()))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp TagListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Tags) != 0 {
		t.Fatalf("expected 0 tags for org B, got %d: %+v", len(resp.Tags), resp.Tags)
	}
}

func TestTenant_ListTeams_Isolated(t *testing.T) {
	h, repo := newTestHandler(t)
	orgA, orgB, _ := setupTwoOrgs(t, repo)
	ctx := context.Background()

	if _, err := repo.CreateTeam(ctx, orgA, "Support"); err != nil {
		t.Fatalf("create team: %v", err)
	}

	r := chi.NewRouter()
	r.Get("/teams", h.ListTeams)

	req := httptest.NewRequest("GET", "/teams", nil)
	req = req.WithContext(principalCtx(orgB, uuid.New()))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp TeamListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Teams) != 0 {
		t.Fatalf("expected 0 teams for org B, got %d: %+v", len(resp.Teams), resp.Teams)
	}
}
