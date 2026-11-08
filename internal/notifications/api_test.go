package notifications

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/HenriNorman/gregport/internal/platform/requestctx"
)

func apiRouter(h *Handler) http.Handler {
	r := chi.NewRouter()
	r.Route("/v1", func(r chi.Router) {
		r.Route("/notifications", func(r chi.Router) {
			r.Get("/", h.List)
			r.Post("/read-all", h.MarkAllRead)
			r.Post("/{id}/read", h.MarkRead)
		})
		r.Route("/notification-preferences", func(r chi.Router) {
			r.Get("/", h.GetPreferences)
			r.Put("/", h.UpdatePreferences)
		})
	})
	return r
}

func apiRequest(t *testing.T, router http.Handler, p *requestctx.Principal, method, path string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		reader = bytes.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if p != nil {
		req = req.WithContext(requestctx.WithPrincipal(req.Context(), *p))
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func decodeError(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode error body %q: %v", rec.Body.String(), err)
	}
	return envelope.Error.Code
}

func testPrincipal() requestctx.Principal {
	return requestctx.Principal{
		Kind:    requestctx.KindUser,
		ActorID: uuid.New(),
		OrgID:   uuid.New(),
		Role:    "agent",
	}
}

func TestAPIListNotifications(t *testing.T) {
	p := testPrincipal()
	ticketID := uuid.New()
	repo := &fakeRepo{
		listResult: []Notification{{
			ID:       uuid.New(),
			OrgID:    p.OrgID,
			UserID:   p.ActorID,
			Type:     EventNewComment,
			TicketID: &ticketID,
			Payload:  []byte(`{"ticket_number":1042}`),
		}},
		listNext: "next",
	}
	rec := apiRequest(t, apiRouter(NewHandler(NewService(repo))), &p,
		"GET", "/v1/notifications?unread=true&limit=10&cursor=abc", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp NotificationListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Notifications) != 1 || resp.NextCursor != "next" {
		t.Fatalf("response: %+v", resp)
	}
	if repo.lastListOrg != p.OrgID || repo.lastListUID != p.ActorID {
		t.Fatalf("tenant scoping: org=%s user=%s", repo.lastListOrg, repo.lastListUID)
	}
	if !repo.lastUnread || repo.lastLimit != 10 || repo.lastCursor != "abc" {
		t.Fatalf("query mapping: unread=%v limit=%d cursor=%q", repo.lastUnread, repo.lastLimit, repo.lastCursor)
	}
}

func TestAPIListNotificationsRequiresAuth(t *testing.T) {
	rec := apiRequest(t, apiRouter(NewHandler(NewService(&fakeRepo{}))), nil,
		"GET", "/v1/notifications", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401", rec.Code)
	}
}

func TestAPIListNotificationsInvalidCursor(t *testing.T) {
	p := testPrincipal()
	repo := &fakeRepo{listErr: ErrInvalidCursor}
	rec := apiRequest(t, apiRouter(NewHandler(NewService(repo))), &p,
		"GET", "/v1/notifications?cursor=not-base64!", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if code := decodeError(t, rec); code != "invalid_cursor" {
		t.Fatalf("code=%q, want invalid_cursor", code)
	}
}

func TestAPIListNotificationsDefaultLimit(t *testing.T) {
	p := testPrincipal()
	repo := &fakeRepo{}
	rec := apiRequest(t, apiRouter(NewHandler(NewService(repo))), &p,
		"GET", "/v1/notifications?limit=999", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	if repo.lastLimit != 100 {
		t.Fatalf("limit=%d, want clamped 100", repo.lastLimit)
	}
}

func TestAPIMarkRead(t *testing.T) {
	p := testPrincipal()
	id := uuid.New()
	repo := &fakeRepo{}
	rec := apiRequest(t, apiRouter(NewHandler(NewService(repo))), &p,
		"POST", "/v1/notifications/"+id.String()+"/read", nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if repo.lastMarkOrg != p.OrgID || repo.lastMarkUID != p.ActorID || repo.lastMarkID != id {
		t.Fatalf("mark args: org=%s user=%s id=%s", repo.lastMarkOrg, repo.lastMarkUID, repo.lastMarkID)
	}
}

func TestAPIMarkReadInvalidID(t *testing.T) {
	p := testPrincipal()
	rec := apiRequest(t, apiRouter(NewHandler(NewService(&fakeRepo{}))), &p,
		"POST", "/v1/notifications/not-a-uuid/read", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if code := decodeError(t, rec); code != "invalid_id" {
		t.Fatalf("code=%q", code)
	}
}

func TestAPIMarkReadNotFound(t *testing.T) {
	p := testPrincipal()
	repo := &fakeRepo{markReadErr: ErrNotificationNotFound}
	rec := apiRequest(t, apiRouter(NewHandler(NewService(repo))), &p,
		"POST", "/v1/notifications/"+uuid.NewString()+"/read", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if code := decodeError(t, rec); code != "notification_not_found" {
		t.Fatalf("code=%q", code)
	}
}

func TestAPIMarkAllRead(t *testing.T) {
	p := testPrincipal()
	repo := &fakeRepo{markAllReadN: 7}
	rec := apiRequest(t, apiRouter(NewHandler(NewService(repo))), &p,
		"POST", "/v1/notifications/read-all", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp MarkAllReadResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Updated != 7 {
		t.Fatalf("updated=%d, want 7", resp.Updated)
	}
	if repo.lastMarkOrg != p.OrgID || repo.lastMarkUID != p.ActorID {
		t.Fatalf("scope: org=%s user=%s", repo.lastMarkOrg, repo.lastMarkUID)
	}
}

func TestAPIGetPreferencesDefaults(t *testing.T) {
	p := testPrincipal()
	rec := apiRequest(t, apiRouter(NewHandler(NewService(&fakeRepo{}))), &p,
		"GET", "/v1/notification-preferences", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp PreferenceListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Preferences) != len(EventTypes())*len(Channels()) {
		t.Fatalf("preferences=%d, want full matrix", len(resp.Preferences))
	}
	for _, pref := range resp.Preferences {
		if !pref.Enabled {
			t.Fatalf("default should be enabled: %+v", pref)
		}
	}
}

func TestAPIUpdatePreferences(t *testing.T) {
	p := testPrincipal()
	body := []byte(`{"preferences":[
		{"event_type":"new_comment","channel":"in_app","enabled":false},
		{"event_type":"sla_breach","channel":"email","enabled":true}
	]}`)
	repo := &fakeRepo{}
	rec := apiRequest(t, apiRouter(NewHandler(NewService(repo))), &p,
		"PUT", "/v1/notification-preferences", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	var resp PreferenceListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	got := map[string]bool{}
	for _, pref := range resp.Preferences {
		got[pref.EventType+"/"+pref.Channel] = pref.Enabled
	}
	if got[EventNewComment+"/"+ChannelInApp] {
		t.Fatal("new_comment/in_app should be disabled")
	}
	if !got[EventSLABreach+"/"+ChannelEmail] {
		t.Fatal("sla_breach/email should be enabled")
	}
	// Only the disabled exception is persisted by the repository contract.
	if len(repo.storedPrefs) != 2 {
		t.Fatalf("stored %d preferences, want 2", len(repo.storedPrefs))
	}
}

func TestAPIUpdatePreferencesValidation(t *testing.T) {
	p := testPrincipal()
	body := []byte(`{"preferences":[{"event_type":"bogus","channel":"in_app","enabled":false}]}`)
	rec := apiRequest(t, apiRouter(NewHandler(NewService(&fakeRepo{}))), &p,
		"PUT", "/v1/notification-preferences", body)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if code := decodeError(t, rec); code != "validation_failed" {
		t.Fatalf("code=%q", code)
	}
}

func TestAPIUpdatePreferencesMalformedJSON(t *testing.T) {
	p := testPrincipal()
	rec := apiRequest(t, apiRouter(NewHandler(NewService(&fakeRepo{}))), &p,
		"PUT", "/v1/notification-preferences", []byte(`{"preferences":`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAPIPreferencesRequireAuth(t *testing.T) {
	router := apiRouter(NewHandler(NewService(&fakeRepo{})))
	for _, tc := range []struct{ method, path string }{
		{"GET", "/v1/notification-preferences"},
		{"PUT", "/v1/notification-preferences"},
		{"POST", "/v1/notifications/read-all"},
		{"POST", "/v1/notifications/" + uuid.NewString() + "/read"},
	} {
		rec := apiRequest(t, router, nil, tc.method, tc.path, []byte(`{}`))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s: status=%d, want 401", tc.method, tc.path, rec.Code)
		}
	}
}
