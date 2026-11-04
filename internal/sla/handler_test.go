package sla

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/HenriNorman/gregport/internal/auth"
	"github.com/HenriNorman/gregport/internal/platform/requestctx"
)

type fakeService struct {
	policies  []Policy
	listErr   error
	getErr    error
	createErr error
	updateErr error
	deleteErr error

	listOrg   uuid.UUID
	getOrg    uuid.UUID
	getID     uuid.UUID
	createOrg uuid.UUID
	createReq CreatePolicyRequest
	updateOrg uuid.UUID
	updateID  uuid.UUID
	updateReq UpdatePolicyRequest
	deleteOrg uuid.UUID
	deleteID  uuid.UUID
	creates   int
	updates   int
	deletes   int
}

func (f *fakeService) List(_ context.Context, orgID uuid.UUID) ([]Policy, error) {
	f.listOrg = orgID
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.policies, nil
}

func (f *fakeService) Get(_ context.Context, orgID, policyID uuid.UUID) (Policy, error) {
	f.getOrg = orgID
	f.getID = policyID
	if f.getErr != nil {
		return Policy{}, f.getErr
	}
	return Policy{ID: policyID, Name: "Default", Priority: "normal"}, nil
}

func (f *fakeService) Create(_ context.Context, orgID uuid.UUID, req CreatePolicyRequest) (Policy, error) {
	f.createOrg = orgID
	f.createReq = req
	f.creates++
	if f.createErr != nil {
		return Policy{}, f.createErr
	}
	return Policy{
		ID:                   uuid.New(),
		OrgID:                orgID,
		Name:                 req.Name,
		Priority:             req.Priority,
		FirstResponseMinutes: req.FirstResponseMinutes,
		ResolutionMinutes:    req.ResolutionMinutes,
		CreatedAt:            time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC),
	}, nil
}

func (f *fakeService) Update(_ context.Context, orgID, policyID uuid.UUID, req UpdatePolicyRequest) (Policy, error) {
	f.updateOrg = orgID
	f.updateID = policyID
	f.updateReq = req
	f.updates++
	if f.updateErr != nil {
		return Policy{}, f.updateErr
	}
	return Policy{ID: policyID, OrgID: orgID, Name: "Updated", Priority: "high"}, nil
}

func (f *fakeService) Delete(_ context.Context, orgID, policyID uuid.UUID) error {
	f.deleteOrg = orgID
	f.deleteID = policyID
	f.deletes++
	return f.deleteErr
}

func newSLAHandler(svc policyService) http.Handler {
	r := chi.NewRouter()
	h := NewHandler(svc)
	r.Get("/sla-policies", h.ListPolicies)
	r.With(auth.RequireRole("owner", "admin")).Post("/sla-policies", h.CreatePolicy)
	r.Get("/sla-policies/{id}", h.GetPolicy)
	r.With(auth.RequireRole("owner", "admin")).Patch("/sla-policies/{id}", h.UpdatePolicy)
	r.With(auth.RequireRole("owner", "admin")).Delete("/sla-policies/{id}", h.DeletePolicy)
	return r
}

func slaRequest(method, target string, body []byte, orgID uuid.UUID, role string) *http.Request {
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req.WithContext(requestctx.WithPrincipal(req.Context(), requestctx.Principal{
		Kind:    requestctx.KindUser,
		ActorID: uuid.New(),
		OrgID:   orgID,
		Role:    role,
	}))
}

func decodePolicies(t *testing.T, rec *httptest.ResponseRecorder) PolicyListResponse {
	t.Helper()

	var resp PolicyListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode policies: %v (body=%s)", err, rec.Body.String())
	}
	return resp
}

func decodePolicy(t *testing.T, rec *httptest.ResponseRecorder) PolicyDTO {
	t.Helper()

	var resp PolicyDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode policy: %v (body=%s)", err, rec.Body.String())
	}
	return resp
}

func decodeErrorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()

	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode error: %v (body=%s)", err, rec.Body.String())
	}
	return envelope.Error.Code
}

func TestListPoliciesSuccess(t *testing.T) {
	orgID := uuid.New()
	svc := &fakeService{policies: []Policy{{
		ID: uuid.New(), OrgID: orgID, Name: "Low", Priority: "low",
		FirstResponseMinutes: 60, ResolutionMinutes: 480,
		CreatedAt: time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC),
	}}}
	rec := httptest.NewRecorder()

	newSLAHandler(svc).ServeHTTP(rec, slaRequest(http.MethodGet, "/sla-policies", nil, orgID, "agent"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d body=%s", rec.Code, rec.Body.String())
	}
	if svc.listOrg != orgID {
		t.Fatalf("org: %s, want %s", svc.listOrg, orgID)
	}
	resp := decodePolicies(t, rec)
	if len(resp.Policies) != 1 || resp.Policies[0].Name != "Low" {
		t.Fatalf("policies: %+v", resp.Policies)
	}
}

func TestListPoliciesReturnsEmptyArray(t *testing.T) {
	rec := httptest.NewRecorder()

	newSLAHandler(&fakeService{}).ServeHTTP(rec,
		slaRequest(http.MethodGet, "/sla-policies", nil, uuid.New(), "agent"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	if got := rec.Body.String(); got != "{\"policies\":[]}\n" {
		t.Fatalf("body: %q", got)
	}
}

func TestListPoliciesRequiresPrincipal(t *testing.T) {
	svc := &fakeService{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/sla-policies", nil)

	newSLAHandler(svc).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status: %d", rec.Code)
	}
	if svc.listOrg != uuid.Nil {
		t.Fatal("service was called without a principal")
	}
}

func TestCreatePolicySuccess(t *testing.T) {
	orgID := uuid.New()
	svc := &fakeService{}
	rec := httptest.NewRecorder()

	body := []byte(`{"name":"Urgent","priority":"urgent","first_response_minutes":15,"resolution_minutes":120}`)
	newSLAHandler(svc).ServeHTTP(rec, slaRequest(http.MethodPost, "/sla-policies", body, orgID, "admin"))

	if rec.Code != http.StatusCreated {
		t.Fatalf("status: %d body=%s", rec.Code, rec.Body.String())
	}
	if svc.createOrg != orgID || svc.createReq.Priority != "urgent" ||
		svc.createReq.FirstResponseMinutes != 15 || svc.createReq.ResolutionMinutes != 120 {
		t.Fatalf("service call: org=%s req=%+v", svc.createOrg, svc.createReq)
	}
	resp := decodePolicy(t, rec)
	if resp.Name != "Urgent" || resp.Priority != "urgent" {
		t.Fatalf("response: %+v", resp)
	}
}

func TestCreatePolicyRejectsBadJSON(t *testing.T) {
	tests := []string{
		`not-json`,
		``,
		`{"name":"P","priority":"low","unknown":true}`,
		`{"name":"P","priority":"low","first_response_minutes":"soon"}`,
	}
	for _, body := range tests {
		svc := &fakeService{}
		rec := httptest.NewRecorder()

		newSLAHandler(svc).ServeHTTP(rec,
			slaRequest(http.MethodPost, "/sla-policies", []byte(body), uuid.New(), "owner"))

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body %q: status %d", body, rec.Code)
		}
		if svc.creates != 0 {
			t.Fatalf("body %q: service was called", body)
		}
	}
}

func TestCreatePolicyErrors(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"conflict", ErrPolicyExists, http.StatusConflict, "sla_policy_exists"},
		{"invalid", ErrInvalidInput, http.StatusUnprocessableEntity, "invalid_sla_policy"},
		{"internal", errors.New("db down"), http.StatusInternalServerError, "internal"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &fakeService{createErr: tt.err}
			rec := httptest.NewRecorder()
			body := []byte(`{"name":"P","priority":"low","first_response_minutes":1,"resolution_minutes":2}`)

			newSLAHandler(svc).ServeHTTP(rec,
				slaRequest(http.MethodPost, "/sla-policies", body, uuid.New(), "owner"))

			if rec.Code != tt.wantStatus {
				t.Fatalf("status: %d body=%s", rec.Code, rec.Body.String())
			}
			if code := decodeErrorCode(t, rec); code != tt.wantCode {
				t.Fatalf("code: %q, want %q", code, tt.wantCode)
			}
		})
	}
}

func TestGetPolicySuccessAndNotFound(t *testing.T) {
	orgID := uuid.New()
	policyID := uuid.New()

	svc := &fakeService{}
	rec := httptest.NewRecorder()
	newSLAHandler(svc).ServeHTTP(rec,
		slaRequest(http.MethodGet, "/sla-policies/"+policyID.String(), nil, orgID, "agent"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d body=%s", rec.Code, rec.Body.String())
	}
	if svc.getOrg != orgID || svc.getID != policyID {
		t.Fatalf("service call: org=%s id=%s", svc.getOrg, svc.getID)
	}

	notFound := &fakeService{getErr: ErrNotFound}
	rec = httptest.NewRecorder()
	newSLAHandler(notFound).ServeHTTP(rec,
		slaRequest(http.MethodGet, "/sla-policies/"+policyID.String(), nil, orgID, "agent"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("not found status: %d", rec.Code)
	}
	if code := decodeErrorCode(t, rec); code != "sla_policy_not_found" {
		t.Fatalf("code: %q", code)
	}
}

func TestGetPolicyRejectsInvalidID(t *testing.T) {
	svc := &fakeService{}
	rec := httptest.NewRecorder()

	newSLAHandler(svc).ServeHTTP(rec,
		slaRequest(http.MethodGet, "/sla-policies/not-a-uuid", nil, uuid.New(), "agent"))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: %d", rec.Code)
	}
	if code := decodeErrorCode(t, rec); code != "invalid_id" {
		t.Fatalf("code: %q", code)
	}
	if svc.getID != uuid.Nil {
		t.Fatal("service was called with an invalid id")
	}
}

func TestUpdatePolicySuccess(t *testing.T) {
	orgID := uuid.New()
	policyID := uuid.New()
	svc := &fakeService{}
	rec := httptest.NewRecorder()

	body := []byte(`{"first_response_minutes":10}`)
	newSLAHandler(svc).ServeHTTP(rec,
		slaRequest(http.MethodPatch, "/sla-policies/"+policyID.String(), body, orgID, "owner"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d body=%s", rec.Code, rec.Body.String())
	}
	if svc.updateOrg != orgID || svc.updateID != policyID {
		t.Fatalf("service call: org=%s id=%s", svc.updateOrg, svc.updateID)
	}
	if svc.updateReq.FirstResponseMinutes == nil || *svc.updateReq.FirstResponseMinutes != 10 {
		t.Fatalf("request: %+v", svc.updateReq)
	}
	if svc.updateReq.Name != nil || svc.updateReq.Priority != nil || svc.updateReq.ResolutionMinutes != nil {
		t.Fatalf("omitted fields must stay nil: %+v", svc.updateReq)
	}
}

func TestUpdatePolicyErrors(t *testing.T) {
	policyID := uuid.New()
	tests := []struct {
		name       string
		err        error
		wantStatus int
	}{
		{"not found", ErrNotFound, http.StatusNotFound},
		{"conflict", ErrPolicyExists, http.StatusConflict},
		{"invalid", ErrInvalidInput, http.StatusUnprocessableEntity},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &fakeService{updateErr: tt.err}
			rec := httptest.NewRecorder()

			newSLAHandler(svc).ServeHTTP(rec,
				slaRequest(http.MethodPatch, "/sla-policies/"+policyID.String(),
					[]byte(`{"name":"new"}`), uuid.New(), "owner"))

			if rec.Code != tt.wantStatus {
				t.Fatalf("status: %d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestDeletePolicySuccessAndInUse(t *testing.T) {
	orgID := uuid.New()
	policyID := uuid.New()

	svc := &fakeService{}
	rec := httptest.NewRecorder()
	newSLAHandler(svc).ServeHTTP(rec,
		slaRequest(http.MethodDelete, "/sla-policies/"+policyID.String(), nil, orgID, "admin"))

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status: %d body=%s", rec.Code, rec.Body.String())
	}
	if svc.deleteOrg != orgID || svc.deleteID != policyID {
		t.Fatalf("service call: org=%s id=%s", svc.deleteOrg, svc.deleteID)
	}

	inUse := &fakeService{deleteErr: ErrPolicyInUse}
	rec = httptest.NewRecorder()
	newSLAHandler(inUse).ServeHTTP(rec,
		slaRequest(http.MethodDelete, "/sla-policies/"+policyID.String(), nil, orgID, "owner"))
	if rec.Code != http.StatusConflict {
		t.Fatalf("in-use status: %d", rec.Code)
	}
	if code := decodeErrorCode(t, rec); code != "sla_policy_in_use" {
		t.Fatalf("code: %q", code)
	}
}

func TestSLAMutationsRoleEnforcement(t *testing.T) {
	tests := []struct {
		method     string
		role       string
		wantStatus int
		wantWrites int
	}{
		{http.MethodPost, "owner", http.StatusCreated, 1},
		{http.MethodPost, "admin", http.StatusCreated, 1},
		{http.MethodPost, "agent", http.StatusForbidden, 0},
		{http.MethodPatch, "owner", http.StatusOK, 1},
		{http.MethodPatch, "admin", http.StatusOK, 1},
		{http.MethodPatch, "agent", http.StatusForbidden, 0},
		{http.MethodDelete, "owner", http.StatusNoContent, 1},
		{http.MethodDelete, "admin", http.StatusNoContent, 1},
		{http.MethodDelete, "agent", http.StatusForbidden, 0},
	}

	for _, tt := range tests {
		t.Run(tt.method+"/"+tt.role, func(t *testing.T) {
			svc := &fakeService{}
			rec := httptest.NewRecorder()

			target := "/sla-policies"
			body := []byte(`{"name":"P","priority":"low","first_response_minutes":1,"resolution_minutes":2}`)
			if tt.method != http.MethodPost {
				target += "/" + uuid.NewString()
				body = []byte(`{"name":"P"}`)
			}

			newSLAHandler(svc).ServeHTTP(rec, slaRequest(tt.method, target, body, uuid.New(), tt.role))

			if rec.Code != tt.wantStatus {
				t.Fatalf("status: %d body=%s", rec.Code, rec.Body.String())
			}
			if got := svc.creates + svc.updates + svc.deletes; got != tt.wantWrites {
				t.Fatalf("write calls: %d, want %d", got, tt.wantWrites)
			}
		})
	}
}
