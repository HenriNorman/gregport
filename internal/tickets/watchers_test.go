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

func TestAddWatcher_HappyPath(t *testing.T) {
	h, repo := newTestHandler(t)
	orgA, _, number := setupTwoOrgs(t, repo)
	watcherID := seedMembership(t, repo, orgA)

	r := chi.NewRouter()
	r.Put("/tickets/{number}/watchers/{userId}", h.AddWatcher)

	req := httptest.NewRequest("PUT",
		"/tickets/"+strconv.FormatInt(number, 10)+"/watchers/"+watcherID.String(), nil)
	req = req.WithContext(principalCtx(orgA, uuid.New()))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}

	var count int
	_ = repo.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM ticket_watchers w
		 JOIN tickets t ON t.id = w.ticket_id
		 WHERE t.number = $1 AND w.user_id = $2`, number, watcherID).Scan(&count)
	if count != 1 {
		t.Fatalf("expected 1 watcher, got %d", count)
	}
}

func TestAddWatcher_Idempotent(t *testing.T) {
	h, repo := newTestHandler(t)
	orgA, _, number := setupTwoOrgs(t, repo)
	watcherID := seedMembership(t, repo, orgA)

	r := chi.NewRouter()
	r.Put("/tickets/{number}/watchers/{userId}", h.AddWatcher)

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("PUT",
			"/tickets/"+strconv.FormatInt(number, 10)+"/watchers/"+watcherID.String(), nil)
		req = req.WithContext(principalCtx(orgA, uuid.New()))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("add %d: expected 204, got %d", i, rec.Code)
		}
	}

	var count int
	_ = repo.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM ticket_watchers w
		 JOIN tickets t ON t.id = w.ticket_id
		 WHERE t.number = $1 AND w.user_id = $2`, number, watcherID).Scan(&count)
	if count != 1 {
		t.Fatalf("expected exactly 1 watcher after idempotent add, got %d", count)
	}
}

func TestAddWatcher_NonMember_422(t *testing.T) {
	h, repo := newTestHandler(t)
	orgA, _, number := setupTwoOrgs(t, repo)

	r := chi.NewRouter()
	r.Put("/tickets/{number}/watchers/{userId}", h.AddWatcher)

	req := httptest.NewRequest("PUT",
		"/tickets/"+strconv.FormatInt(number, 10)+"/watchers/"+uuid.New().String(), nil)
	req = req.WithContext(principalCtx(orgA, uuid.New()))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d", rec.Code)
	}
}

func TestRemoveWatcher_Idempotent(t *testing.T) {
	h, repo := newTestHandler(t)
	orgA, _, number := setupTwoOrgs(t, repo)
	watcherID := seedMembership(t, repo, orgA)

	if err := repo.AddWatcher(context.Background(), orgA, number, watcherID); err != nil {
		t.Fatalf("seed watcher: %v", err)
	}

	r := chi.NewRouter()
	r.Delete("/tickets/{number}/watchers/{userId}", h.RemoveWatcher)

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("DELETE",
			"/tickets/"+strconv.FormatInt(number, 10)+"/watchers/"+watcherID.String(), nil)
		req = req.WithContext(principalCtx(orgA, uuid.New()))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("remove %d: expected 204, got %d", i, rec.Code)
		}
	}
}

func TestRemoveWatcher_OtherOrgTicket_404(t *testing.T) {
	h, repo := newTestHandler(t)
	_, orgB, number := setupTwoOrgs(t, repo)

	r := chi.NewRouter()
	r.Delete("/tickets/{number}/watchers/{userId}", h.RemoveWatcher)

	req := httptest.NewRequest("DELETE",
		"/tickets/"+strconv.FormatInt(number, 10)+"/watchers/"+uuid.New().String(), nil)
	req = req.WithContext(principalCtx(orgB, uuid.New()))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestCreateComment_AutoWatchesCommenter(t *testing.T) {
	h, repo := newTestHandler(t)
	orgA, _, number := setupTwoOrgs(t, repo)
	commenterID := seedMembership(t, repo, orgA)

	r := chi.NewRouter()
	r.Post("/tickets/{number}/comments", h.CreateComment)

	body, _ := json.Marshal(CreateCommentRequest{Body: "on it"})
	req := httptest.NewRequest("POST",
		"/tickets/"+strconv.FormatInt(number, 10)+"/comments", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(principalCtx(orgA, commenterID))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var count int
	_ = repo.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM ticket_watchers w
		 JOIN tickets t ON t.id = w.ticket_id
		 WHERE t.number = $1 AND w.user_id = $2`, number, commenterID).Scan(&count)
	if count != 1 {
		t.Fatalf("expected commenter to be a watcher, got %d", count)
	}
}
