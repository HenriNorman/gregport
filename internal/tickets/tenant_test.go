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

	"github.com/HenriNorman/gregport/internal/platform/requestctx"
)

func setupTwoOrgs(t *testing.T, repo *Repository) (orgA, orgB uuid.UUID, number int64) {
	t.Helper()
	pool := repo.pool
	ctx := context.Background()

	if err := pool.QueryRow(ctx,
		`INSERT INTO organizations (name, slug) VALUES ('Tenant A', $1) RETURNING id`,
		"ta-"+uuid.NewString()[:8],
	).Scan(&orgA); err != nil {
		t.Fatalf("org A: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO organizations (name, slug) VALUES ('Tenant B', $1) RETURNING id`,
		"tb-"+uuid.NewString()[:8],
	).Scan(&orgB); err != nil {
		t.Fatalf("org B: %v", err)
	}

	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM ticket_events WHERE org_id IN ($1, $2)`, orgA, orgB)
		_, _ = pool.Exec(bg, `DELETE FROM comments WHERE org_id IN ($1, $2)`, orgA, orgB)
		_, _ = pool.Exec(bg, `DELETE FROM tickets WHERE org_id IN ($1, $2)`, orgA, orgB)
		_, _ = pool.Exec(bg, `DELETE FROM customers WHERE org_id IN ($1, $2)`, orgA, orgB)
		_, _ = pool.Exec(bg, `DELETE FROM organizations WHERE id IN ($1, $2)`, orgA, orgB)
	})

	tk, _, err := repo.CreateTicket(ctx, CreateInput{
		OrgID: orgA, Actor: Actor{Type: "system"},
		RequesterEmail: "req@test.local", RequesterName: "Req",
		Subject: "tenant test", Body: "b", Priority: "normal",
	})
	if err != nil {
		t.Fatalf("create ticket: %v", err)
	}
	return orgA, orgB, tk.Number
}

func principalCtx(orgID, userID uuid.UUID) context.Context {
	return requestctx.WithPrincipal(context.Background(), requestctx.Principal{
		UserID: userID,
		OrgID:  orgID,
		Role:   "owner",
	})
}

func newTestHandler(t *testing.T) (*Handler, *Repository) {
	t.Helper()
	pool := testPool(t)
	repo := NewRepository(pool)
	return NewHandler(NewService(repo)), repo
}

func TestTenant_GetTicket_OtherOrg_404(t *testing.T) {
	h, repo := newTestHandler(t)
	_, orgB, number := setupTwoOrgs(t, repo)

	r := chi.NewRouter()
	r.Get("/tickets/{number}", h.Get)

	req := httptest.NewRequest("GET", "/tickets/"+strconv.FormatInt(number, 10), nil)
	req = req.WithContext(principalCtx(orgB, uuid.New()))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestTenant_UpdateTicket_OtherOrg_404(t *testing.T) {
	h, repo := newTestHandler(t)
	_, orgB, number := setupTwoOrgs(t, repo)

	r := chi.NewRouter()
	r.Patch("/tickets/{number}", h.Update)

	body, _ := json.Marshal(UpdateTicketRequest{Subject: strPtr("hacked")})
	req := httptest.NewRequest("PATCH", "/tickets/"+strconv.FormatInt(number, 10), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("If-Match", `"1"`)
	req = req.WithContext(principalCtx(orgB, uuid.New()))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestTenant_DeleteTicket_OtherOrg_404(t *testing.T) {
	h, repo := newTestHandler(t)
	_, orgB, number := setupTwoOrgs(t, repo)

	r := chi.NewRouter()
	r.Delete("/tickets/{number}", h.Delete)

	req := httptest.NewRequest("DELETE", "/tickets/"+strconv.FormatInt(number, 10), nil)
	req = req.WithContext(principalCtx(orgB, uuid.New()))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestTenant_CreateComment_OtherOrg_404(t *testing.T) {
	h, repo := newTestHandler(t)
	_, orgB, number := setupTwoOrgs(t, repo)

	r := chi.NewRouter()
	r.Post("/tickets/{number}/comments", h.CreateComment)

	body, _ := json.Marshal(CreateCommentRequest{Body: "intrusion"})
	req := httptest.NewRequest("POST", "/tickets/"+strconv.FormatInt(number, 10)+"/comments", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(principalCtx(orgB, uuid.New()))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestTenant_ListComments_OtherOrg_404(t *testing.T) {
	h, repo := newTestHandler(t)
	_, orgB, number := setupTwoOrgs(t, repo)

	r := chi.NewRouter()
	r.Get("/tickets/{number}/comments", h.ListComments)

	req := httptest.NewRequest("GET", "/tickets/"+strconv.FormatInt(number, 10)+"/comments", nil)
	req = req.WithContext(principalCtx(orgB, uuid.New()))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func strPtr(s string) *string { return &s }
