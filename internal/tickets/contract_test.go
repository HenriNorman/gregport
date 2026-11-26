package tickets

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/gorillamux"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	apidocs "github.com/HenriNorman/gregport/api"
	"github.com/HenriNorman/gregport/internal/platform/requestctx"
)

// contractRouter serves only the endpoints exercised here. The real router is
// assembled in cmd/api; these tests pin the handler payloads to the published
// specification so a renamed or added field fails CI.
func contractRouter(h *Handler, p requestctx.Principal) http.Handler {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(requestctx.WithPrincipal(req.Context(), p)))
		})
	})
	r.Get("/v1/tickets", h.List)
	r.Post("/v1/tickets", h.Create)
	r.Get("/v1/tickets/{number}", h.Get)
	return r
}

func loadContract(t *testing.T) (*openapi3.T, routers.Router) {
	t.Helper()
	loader := openapi3.NewLoader()
	loader.IsExternalRefsAllowed = false
	doc, err := loader.LoadFromData(apidocs.Spec)
	if err != nil {
		t.Fatalf("load api/openapi.yaml: %v", err)
	}
	if err := doc.Validate(loader.Context); err != nil {
		t.Fatalf("validate api/openapi.yaml: %v", err)
	}
	router, err := gorillamux.NewRouter(doc)
	if err != nil {
		t.Fatalf("build OpenAPI router: %v", err)
	}
	return doc, router
}

func assertContract(t *testing.T, specRouter routers.Router, req *http.Request, rec *httptest.ResponseRecorder) {
	t.Helper()
	req.Host = "localhost:8080"
	route, pathParams, err := specRouter.FindRoute(req)
	if err != nil {
		t.Fatalf("no OpenAPI route for %s %s: %v", req.Method, req.URL.Path, err)
	}
	err = openapi3filter.ValidateResponse(context.Background(), &openapi3filter.ResponseValidationInput{
		RequestValidationInput: &openapi3filter.RequestValidationInput{
			Request:    req,
			PathParams: pathParams,
			Route:      route,
			Options:    &openapi3filter.Options{MultiError: true},
		},
		Status:  rec.Code,
		Header:  rec.Header(),
		Body:    io.NopCloser(bytes.NewReader(rec.Body.Bytes())),
		Options: &openapi3filter.Options{MultiError: true},
	})
	if err != nil {
		t.Fatalf("%s %s response drifted from the spec: %v\nbody: %s",
			req.Method, req.URL.Path, err, rec.Body.String())
	}
}

func contractFixture(t *testing.T) (*Repository, uuid.UUID, uuid.UUID) {
	t.Helper()
	pool := testPool(t)
	repo := NewRepository(pool)
	ctx := context.Background()

	var orgID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO organizations (name, slug) VALUES ('Contract Test', $1) RETURNING id`,
		"contract-"+uuid.NewString()[:8],
	).Scan(&orgID); err != nil {
		t.Fatalf("org: %v", err)
	}
	actorID := seedMembership(t, repo, orgID)
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM ticket_events WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(bg, `DELETE FROM comments WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(bg, `DELETE FROM tickets WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(bg, `DELETE FROM customers WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(bg, `DELETE FROM memberships WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(bg, `DELETE FROM organizations WHERE id = $1`, orgID)
	})
	return repo, orgID, actorID
}

func TestContract_TicketListAndDetail(t *testing.T) {
	_, specRouter := loadContract(t)
	repo, orgID, actorID := contractFixture(t)
	ctx := context.Background()

	if _, _, err := repo.CreateTicket(ctx, CreateInput{
		OrgID:          orgID,
		Actor:          Actor{Type: "system"},
		RequesterEmail: "contract@test.local",
		RequesterName:  "Contract",
		Subject:        "Contract subject",
		Body:           "Contract body",
		Priority:       "normal",
	}); err != nil {
		t.Fatalf("seed ticket: %v", err)
	}

	svc := NewService(repo, NewInvitationTokenManager(0))
	handler := NewHandler(svc)
	router := contractRouter(handler, principalOf(orgID, actorID, "agent"))

	listReq := httptest.NewRequest(http.MethodGet, "/v1/tickets?limit=10", nil)
	listRec := httptest.NewRecorder()
	router.ServeHTTP(listRec, listReq)
	assertContract(t, specRouter, listReq, listRec)

	var list TicketListResponse
	if err := json.Unmarshal(listRec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list.Tickets) != 1 {
		t.Fatalf("expected one ticket, got %d", len(list.Tickets))
	}

	getReq := httptest.NewRequest(http.MethodGet,
		"/v1/tickets/"+strconv.FormatInt(list.Tickets[0].Number, 10), nil)
	getRec := httptest.NewRecorder()
	router.ServeHTTP(getRec, getReq)
	assertContract(t, specRouter, getReq, getRec)
}

func TestContract_TicketCreate(t *testing.T) {
	_, specRouter := loadContract(t)
	repo, orgID, actorID := contractFixture(t)

	svc := NewService(repo, NewInvitationTokenManager(0))
	handler := NewHandler(svc)
	router := contractRouter(handler, principalOf(orgID, actorID, "agent"))

	body := []byte(`{"requester_email":"contract@test.local","requester_name":"Contract",` +
		`"subject":"Created through the contract","body":"Body","priority":"high"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/tickets", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	assertContract(t, specRouter, req, rec)
}
