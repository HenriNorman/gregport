package tickets

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/HenriNorman/gregport/internal/auth"
	"github.com/HenriNorman/gregport/internal/platform/requestctx"
)

func testTokenManager(t *testing.T) *auth.TokenManager {
	t.Helper()
	return auth.NewTokenManager(
		"0123456789abcdef0123456789abcdef",
		15*time.Minute,
		30*24*time.Hour,
	)
}

func tokenFor(t *testing.T, tm *auth.TokenManager, userID, orgID uuid.UUID, role string) string {
	t.Helper()
	tok, _, err := tm.IssueAccess(userID, orgID, role)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	return tok
}

// withAuth sets the Authorization header and the principal in the context.
// Used for tests that call the handler directly (no Authenticate middleware).
func withAuth(req *http.Request, tok string, p requestctx.Principal) *http.Request {
	req.Header.Set("Authorization", "Bearer "+tok)
	return req.WithContext(requestctx.WithPrincipal(req.Context(), p))
}

func TestAuthz_DeleteTicket_Agent_403(t *testing.T) {
	h, repo := newTestHandler(t)
	orgA, _, number := setupTwoOrgs(t, repo)

	tm := testTokenManager(t)
	userID := uuid.New()
	tok := tokenFor(t, tm, userID, orgA, "agent")

	r := chi.NewRouter()
	r.With(auth.RequireRole("owner", "admin")).
		Delete("/tickets/{number}", h.Delete)

	req := httptest.NewRequest("DELETE", "/tickets/"+strconv.FormatInt(number, 10), nil)
	req = withAuth(req, tok, requestctx.Principal{UserID: userID, OrgID: orgA, Role: "agent"})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for agent, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAuthz_DeleteTicket_Owner_204(t *testing.T) {
	h, repo := newTestHandler(t)
	orgA, _, number := setupTwoOrgs(t, repo)

	tm := testTokenManager(t)
	userID := uuid.New()
	tok := tokenFor(t, tm, userID, orgA, "owner")

	r := chi.NewRouter()
	r.With(auth.RequireRole("owner", "admin")).
		Delete("/tickets/{number}", h.Delete)

	req := httptest.NewRequest("DELETE", "/tickets/"+strconv.FormatInt(number, 10), nil)
	req = withAuth(req, tok, requestctx.Principal{UserID: userID, OrgID: orgA, Role: "owner"})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 for owner, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAuthz_DeleteTicket_Admin_204(t *testing.T) {
	h, repo := newTestHandler(t)
	orgA, _, number := setupTwoOrgs(t, repo)

	tm := testTokenManager(t)
	userID := uuid.New()
	tok := tokenFor(t, tm, userID, orgA, "admin")

	r := chi.NewRouter()
	r.With(auth.RequireRole("owner", "admin")).
		Delete("/tickets/{number}", h.Delete)

	req := httptest.NewRequest("DELETE", "/tickets/"+strconv.FormatInt(number, 10), nil)
	req = withAuth(req, tok, requestctx.Principal{UserID: userID, OrgID: orgA, Role: "admin"})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 for admin, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestUpdate_MissingIfMatch_428(t *testing.T) {
	h, repo := newTestHandler(t)
	orgA, _, number := setupTwoOrgs(t, repo)

	r := chi.NewRouter()
	r.Patch("/tickets/{number}", h.Update)

	body, _ := json.Marshal(UpdateTicketRequest{Subject: strPtr("x")})
	req := httptest.NewRequest("PATCH", "/tickets/"+strconv.FormatInt(number, 10), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(principalCtx(orgA, uuid.New()))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("expected 428, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestUpdate_StaleIfMatch_412(t *testing.T) {
	h, repo := newTestHandler(t)
	orgA, _, number := setupTwoOrgs(t, repo)

	r := chi.NewRouter()
	r.Patch("/tickets/{number}", h.Update)

	body, _ := json.Marshal(UpdateTicketRequest{Subject: strPtr("x")})
	req := httptest.NewRequest("PATCH", "/tickets/"+strconv.FormatInt(number, 10), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("If-Match", `"999"`)
	req = req.WithContext(principalCtx(orgA, uuid.New()))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("expected 412, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestUpdate_MalformedIfMatch_400(t *testing.T) {
	h, repo := newTestHandler(t)
	orgA, _, number := setupTwoOrgs(t, repo)

	r := chi.NewRouter()
	r.Patch("/tickets/{number}", h.Update)

	body, _ := json.Marshal(UpdateTicketRequest{Subject: strPtr("x")})
	req := httptest.NewRequest("PATCH", "/tickets/"+strconv.FormatInt(number, 10), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("If-Match", "notanint")
	req = req.WithContext(principalCtx(orgA, uuid.New()))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestUpdate_ValidIfMatch_200(t *testing.T) {
	h, repo := newTestHandler(t)
	orgA, _, number := setupTwoOrgs(t, repo)

	r := chi.NewRouter()
	r.Patch("/tickets/{number}", h.Update)

	body, _ := json.Marshal(UpdateTicketRequest{Status: strPtr("open")})
	req := httptest.NewRequest("PATCH", "/tickets/"+strconv.FormatInt(number, 10), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("If-Match", `"1"`)
	req = req.WithContext(principalCtx(orgA, uuid.New()))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("ETag"); got != `"2"` {
		t.Fatalf("expected ETag %q, got %q", `"2"`, got)
	}
}
