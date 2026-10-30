package tickets

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	
	"github.com/HenriNorman/gregport/internal/platform/requestctx"	
	"github.com/HenriNorman/gregport/internal/auth"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func TestCreateTeam_HappyPath(t *testing.T) {
	h, repo := newTestHandler(t)
	orgA, _, _ := setupTwoOrgs(t, repo)

	r := chi.NewRouter()
	r.Post("/teams", h.CreateTeam)

	body, _ := json.Marshal(CreateTeamRequest{Name: "Support"})
	req := httptest.NewRequest("POST", "/teams", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(principalCtx(orgA, uuid.New()))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCreateTeam_DuplicateName_409(t *testing.T) {
	h, repo := newTestHandler(t)
	orgA, _, _ := setupTwoOrgs(t, repo)

	r := chi.NewRouter()
	r.Post("/teams", h.CreateTeam)

	body, _ := json.Marshal(CreateTeamRequest{Name: "Support"})
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("POST", "/teams", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(principalCtx(orgA, uuid.New()))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if i == 1 && rec.Code != http.StatusConflict {
			t.Fatalf("expected 409 on second create, got %d", rec.Code)
		}
	}
}

func TestCreateTeam_Agent_403(t *testing.T) {
	h, repo := newTestHandler(t)
	orgA, _, _ := setupTwoOrgs(t, repo)

	r := chi.NewRouter()
	r.With(requireRoleForTest("owner", "admin")).Post("/teams", h.CreateTeam)

	body, _ := json.Marshal(CreateTeamRequest{Name: "Support"})
	req := httptest.NewRequest("POST", "/teams", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(principalCtxRole(orgA, uuid.New(), "agent"))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for agent, got %d", rec.Code)
	}
}

func TestAddTeamMember_CrossOrg_422(t *testing.T) {
	h, repo := newTestHandler(t)
	orgA, orgB, _ := setupTwoOrgs(t, repo)

	team, err := repo.CreateTeam(context.Background(), orgA, "Support")
	if err != nil {
		t.Fatalf("create team: %v", err)
	}

	userB := seedMembership(t, repo, orgB)

	r := chi.NewRouter()
	r.Put("/teams/{id}/members/{userId}", h.AddTeamMember)

	req := httptest.NewRequest("PUT",
		"/teams/"+team.ID.String()+"/members/"+userB.String(), nil)
	req = req.WithContext(principalCtx(orgA, uuid.New()))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for cross-org member, got %d: %s", rec.Code, rec.Body.String())
	}
}
func principalCtxRole(orgID, userID uuid.UUID, role string) context.Context {
	return requestctx.WithPrincipal(context.Background(), requestctx.Principal{
		UserID: userID, OrgID: orgID, Role: role,
	})
}

func requireRoleForTest(roles ...string) func(http.Handler) http.Handler {
	return auth.RequireRole(roles...)
}
