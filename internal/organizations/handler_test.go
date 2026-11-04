package organizations

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
	getResp  SettingsResponse
	getErr   error
	updResp  SettingsResponse
	updErr   error
	getOrg   uuid.UUID
	updOrg   uuid.UUID
	updReq   UpdateSettingsRequest
	getCalls int
	updCalls int
}

func (f *fakeService) Get(_ context.Context, orgID uuid.UUID) (SettingsResponse, error) {
	f.getCalls++
	f.getOrg = orgID
	if f.getErr != nil {
		return SettingsResponse{}, f.getErr
	}
	return f.getResp, nil
}

func (f *fakeService) Update(_ context.Context, orgID uuid.UUID, req UpdateSettingsRequest) (SettingsResponse, error) {
	f.updCalls++
	f.updOrg = orgID
	f.updReq = req
	if f.updErr != nil {
		return SettingsResponse{}, f.updErr
	}
	return f.updResp, nil
}

func newSettingsHandler(svc settingsService) http.Handler {
	r := chi.NewRouter()
	h := NewHandler(svc)
	r.Get("/organization/settings", h.GetSettings)
	r.With(auth.RequireRole("owner", "admin")).
		Patch("/organization/settings", h.UpdateSettings)
	return r
}

func principalRequest(method, target string, body []byte, orgID uuid.UUID, role string) *http.Request {
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		reader = bytes.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)
	req.Header.Set("Content-Type", "application/json")
	return req.WithContext(requestctx.WithPrincipal(req.Context(), requestctx.Principal{
		Kind:    requestctx.KindUser,
		ActorID: uuid.New(),
		OrgID:   orgID,
		Role:    role,
	}))
}

func decodeSettings(t *testing.T, rec *httptest.ResponseRecorder) SettingsResponse {
	t.Helper()

	var resp SettingsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v (body=%s)", err, rec.Body.String())
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

func TestGetSettingsReturnsTenantSettings(t *testing.T) {
	orgID := uuid.New()
	updatedAt := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	svc := &fakeService{getResp: SettingsResponse{
		Settings:  Settings{Timezone: "Africa/Nairobi", DefaultPriority: "high", SupportEmail: "help@acme.com"},
		UpdatedAt: updatedAt,
	}}
	rec := httptest.NewRecorder()

	newSettingsHandler(svc).ServeHTTP(rec, principalRequest(
		http.MethodGet, "/organization/settings", nil, orgID, "agent"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("content type: %q", got)
	}
	if svc.getOrg != orgID {
		t.Fatalf("service received org %s, want %s", svc.getOrg, orgID)
	}

	resp := decodeSettings(t, rec)
	if resp.Settings.Timezone != "Africa/Nairobi" ||
		resp.Settings.DefaultPriority != "high" ||
		resp.Settings.SupportEmail != "help@acme.com" {
		t.Fatalf("settings: %+v", resp.Settings)
	}
	if !resp.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("updated at: %s", resp.UpdatedAt)
	}
}

func TestGetSettingsRequiresPrincipal(t *testing.T) {
	svc := &fakeService{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/organization/settings", nil)

	newSettingsHandler(svc).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status: %d", rec.Code)
	}
	if code := decodeErrorCode(t, rec); code != "unauthorized" {
		t.Fatalf("code: %q", code)
	}
	if svc.getCalls != 0 {
		t.Fatalf("service was called %d times", svc.getCalls)
	}
}

func TestGetSettingsNotFound(t *testing.T) {
	svc := &fakeService{getErr: ErrOrgNotFound}
	rec := httptest.NewRecorder()

	newSettingsHandler(svc).ServeHTTP(rec, principalRequest(
		http.MethodGet, "/organization/settings", nil, uuid.New(), "agent"))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: %d", rec.Code)
	}
	if code := decodeErrorCode(t, rec); code != "org_not_found" {
		t.Fatalf("code: %q", code)
	}
}

func TestUpdateSettingsAppliesPatch(t *testing.T) {
	orgID := uuid.New()
	svc := &fakeService{updResp: SettingsResponse{
		Settings:  Settings{Timezone: "Europe/London", DefaultPriority: "normal", SupportEmail: "support@acme.com"},
		UpdatedAt: time.Now().UTC(),
	}}
	rec := httptest.NewRecorder()

	body := []byte(`{"timezone":"Europe/London"}`)
	newSettingsHandler(svc).ServeHTTP(rec, principalRequest(
		http.MethodPatch, "/organization/settings", body, orgID, "admin"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d body=%s", rec.Code, rec.Body.String())
	}
	if svc.updCalls != 1 || svc.updOrg != orgID {
		t.Fatalf("service call: calls=%d org=%s", svc.updCalls, svc.updOrg)
	}
	if svc.updReq.Timezone == nil || *svc.updReq.Timezone != "Europe/London" {
		t.Fatalf("timezone passed to service: %+v", svc.updReq.Timezone)
	}
	if svc.updReq.DefaultPriority != nil || svc.updReq.SupportEmail != nil {
		t.Fatalf("omitted fields must stay nil: %+v", svc.updReq)
	}

	resp := decodeSettings(t, rec)
	if resp.Settings.Timezone != "Europe/London" {
		t.Fatalf("response settings: %+v", resp.Settings)
	}
}

func TestUpdateSettingsRequiresPrincipal(t *testing.T) {
	svc := &fakeService{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/organization/settings",
		bytes.NewReader([]byte(`{"timezone":"UTC"}`)))

	newSettingsHandler(svc).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status: %d", rec.Code)
	}
	if svc.updCalls != 0 {
		t.Fatalf("service was called %d times", svc.updCalls)
	}
}

func TestUpdateSettingsRejectsBadJSON(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"malformed", `not-json`},
		{"empty", ``},
		{"unknown field", `{"timezone":"UTC","theme":"dark"}`},
		{"wrong type", `{"timezone":42}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &fakeService{}
			rec := httptest.NewRecorder()

			newSettingsHandler(svc).ServeHTTP(rec, principalRequest(
				http.MethodPatch, "/organization/settings", []byte(tt.body), uuid.New(), "owner"))

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status: %d body=%s", rec.Code, rec.Body.String())
			}
			if svc.updCalls != 0 {
				t.Fatalf("service was called for invalid JSON")
			}
		})
	}
}

func TestUpdateSettingsInvalidInput(t *testing.T) {
	svc := &fakeService{updErr: ErrInvalidInput}
	rec := httptest.NewRecorder()

	newSettingsHandler(svc).ServeHTTP(rec, principalRequest(
		http.MethodPatch, "/organization/settings",
		[]byte(`{"timezone":"Mars/Olympus"}`), uuid.New(), "owner"))

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status: %d", rec.Code)
	}
	if code := decodeErrorCode(t, rec); code != "invalid_settings" {
		t.Fatalf("code: %q", code)
	}
}

func TestUpdateSettingsNotFound(t *testing.T) {
	svc := &fakeService{updErr: ErrOrgNotFound}
	rec := httptest.NewRecorder()

	newSettingsHandler(svc).ServeHTTP(rec, principalRequest(
		http.MethodPatch, "/organization/settings",
		[]byte(`{"timezone":"UTC"}`), uuid.New(), "owner"))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: %d", rec.Code)
	}
}

func TestUpdateSettingsRoleEnforcement(t *testing.T) {
	tests := []struct {
		role       string
		wantStatus int
		wantCalls  int
	}{
		{"owner", http.StatusOK, 1},
		{"admin", http.StatusOK, 1},
		{"agent", http.StatusForbidden, 0},
	}

	for _, tt := range tests {
		t.Run(tt.role, func(t *testing.T) {
			svc := &fakeService{updResp: SettingsResponse{Settings: Settings{Timezone: "UTC"}}}
			rec := httptest.NewRecorder()

			newSettingsHandler(svc).ServeHTTP(rec, principalRequest(
				http.MethodPatch, "/organization/settings",
				[]byte(`{"timezone":"UTC"}`), uuid.New(), tt.role))

			if rec.Code != tt.wantStatus {
				t.Fatalf("status: %d body=%s", rec.Code, rec.Body.String())
			}
			if svc.updCalls != tt.wantCalls {
				t.Fatalf("service calls: %d, want %d", svc.updCalls, tt.wantCalls)
			}
		})
	}
}

func TestUpdateSettingsUnexpectedServiceError(t *testing.T) {
	svc := &fakeService{updErr: errors.New("database down")}
	rec := httptest.NewRecorder()

	newSettingsHandler(svc).ServeHTTP(rec, principalRequest(
		http.MethodPatch, "/organization/settings",
		[]byte(`{"timezone":"UTC"}`), uuid.New(), "owner"))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status: %d", rec.Code)
	}
	if code := decodeErrorCode(t, rec); code != "internal" {
		t.Fatalf("code: %q", code)
	}
}
