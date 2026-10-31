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

func TestAssignAuto_ConcurrentSpread(t *testing.T) {
	h, repo := newTestHandler(t)
	orgA, _, number := setupTwoOrgs(t, repo)

	// team with 3 agents
	team, err := repo.CreateTeam(context.Background(), orgA, "Support")
	if err != nil {
		t.Fatalf("team: %v", err)
	}
	agents := make([]uuid.UUID, 3)
	for i := range agents {
		agents[i] = seedMembership(t, repo, orgA)
		if err := repo.AddTeamMember(context.Background(), orgA, team.ID, agents[i]); err != nil {
			t.Fatalf("add member: %v", err)
		}
	}

	r := chi.NewRouter()
	r.Post("/tickets/{number}/assignment", h.Assign)

	// 20 concurrent auto-assigns, same ticket
	body, _ := json.Marshal(AssignTicketRequest{
		TeamID: strPtr(team.ID.String()),
		Auto:   true,
	})

	var wg sync.WaitGroup
	codes := make([]int, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := httptest.NewRequest("POST",
				"/tickets/"+strconv.FormatInt(number, 10)+"/assignment",
				bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req = req.WithContext(principalCtx(orgA, uuid.New()))
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			codes[i] = rec.Code
		}(i)
	}
	wg.Wait()

	for i, c := range codes {
		if c != http.StatusOK {
			t.Fatalf("request %d: expected 200, got %d", i, c)
		}
	}

	// each agent got at least one assignment's worth of last_assigned_at
	var assigned int
	if err := repo.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM team_members
		 WHERE team_id = $1 AND last_assigned_at IS NOT NULL`,
		team.ID,
	).Scan(&assigned); err != nil {
		t.Fatalf("count assigned: %v", err)
	}
	if assigned != 3 {
		t.Fatalf("expected all 3 agents to be assigned at least once, got %d", assigned)
	}
}

func TestAssignAuto_NoTeamMembers_422(t *testing.T) {
	h, repo := newTestHandler(t)
	orgA, _, number := setupTwoOrgs(t, repo)

	team, err := repo.CreateTeam(context.Background(), orgA, "Empty")
	if err != nil {
		t.Fatalf("team: %v", err)
	}

	r := chi.NewRouter()
	r.Post("/tickets/{number}/assignment", h.Assign)

	body, _ := json.Marshal(AssignTicketRequest{
		TeamID: strPtr(team.ID.String()),
		Auto:   true,
	})
	req := httptest.NewRequest("POST",
		"/tickets/"+strconv.FormatInt(number, 10)+"/assignment",
		bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(principalCtx(orgA, uuid.New()))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d: %s", rec.Code, rec.Body.String())
	}
}
