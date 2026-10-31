package tickets

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func TestAssign_HappyPath_Agent(t *testing.T) {
	h, repo := newTestHandler(t)
	orgA, _, number := setupTwoOrgs(t, repo)
	agentID := seedMembership(t, repo, orgA)

	r := chi.NewRouter()
	r.Post("/tickets/{number}/assignment", h.Assign)

	body, _ := json.Marshal(AssignTicketRequest{AssigneeID: strPtr(agentID.String())})
	req := httptest.NewRequest("POST", "/tickets/"+numStr(number)+"/assignment", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(principalCtx(orgA, uuid.New()))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// event written
	var count int
	_ = repo.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM ticket_events WHERE event_type = 'ticket.assigned'`).Scan(&count)
	if count < 1 {
		t.Fatalf("expected ticket.assigned event, got %d", count)
	}

	// watcher added
	var watchers int
	_ = repo.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM ticket_watchers w
		 JOIN tickets t ON t.id = w.ticket_id
		 WHERE t.number = $1 AND w.user_id = $2`, number, agentID).Scan(&watchers)
	if watchers != 1 {
		t.Fatalf("expected 1 watcher, got %d", watchers)
	}
}

func TestAssign_NonMember_422(t *testing.T) {
	h, repo := newTestHandler(t)
	orgA, _, number := setupTwoOrgs(t, repo)

	r := chi.NewRouter()
	r.Post("/tickets/{number}/assignment", h.Assign)

	body, _ := json.Marshal(AssignTicketRequest{AssigneeID: strPtr(uuid.New().String())})
	req := httptest.NewRequest("POST", "/tickets/"+numStr(number)+"/assignment", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(principalCtx(orgA, uuid.New()))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d", rec.Code)
	}
}

func TestAssign_Team_SetsTeamOnly(t *testing.T) {
	h, repo := newTestHandler(t)
	orgA, _, number := setupTwoOrgs(t, repo)

	team, err := repo.CreateTeam(context.Background(), orgA, "Support")
	if err != nil {
		t.Fatalf("team: %v", err)
	}

	r := chi.NewRouter()
	r.Post("/tickets/{number}/assignment", h.Assign)

	body, _ := json.Marshal(AssignTicketRequest{TeamID: strPtr(team.ID.String())})
	req := httptest.NewRequest("POST", "/tickets/"+numStr(number)+"/assignment", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(principalCtx(orgA, uuid.New()))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAssign_OtherOrgTeam_404(t *testing.T) {
	h, repo := newTestHandler(t)
	orgA, orgB, number := setupTwoOrgs(t, repo)

	teamB, err := repo.CreateTeam(context.Background(), orgB, "Other")
	if err != nil {
		t.Fatalf("team: %v", err)
	}

	r := chi.NewRouter()
	r.Post("/tickets/{number}/assignment", h.Assign)

	body, _ := json.Marshal(AssignTicketRequest{TeamID: strPtr(teamB.ID.String())})
	req := httptest.NewRequest("POST", "/tickets/"+numStr(number)+"/assignment", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(principalCtx(orgA, uuid.New()))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}


func numStr(n int64) string { return strconv.FormatInt(n, 10) }
