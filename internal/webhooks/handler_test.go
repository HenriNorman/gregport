package webhooks

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

type fakeWebhookService struct {
	subs              map[uuid.UUID]Subscription
	listErr           error
	getErr            error
	createErr         error
	updateErr         error
	deleteErr         error
	testErr           error
	deliveries        []Delivery
	listDeliveriesErr error
	redeliverErr      error

	lastOrgID          uuid.UUID
	lastSubscriptionID uuid.UUID
	lastDeliveryID     uuid.UUID
}

func (f *fakeWebhookService) List(_ context.Context, orgID uuid.UUID) ([]Subscription, error) {
	f.lastOrgID = orgID
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]Subscription, 0, len(f.subs))
	for _, sub := range f.subs {
		if sub.OrgID == orgID {
			out = append(out, sub)
		}
	}
	return out, nil
}

func (f *fakeWebhookService) Get(_ context.Context, orgID, id uuid.UUID) (Subscription, error) {
	f.lastOrgID = orgID
	f.lastSubscriptionID = id
	if f.getErr != nil {
		return Subscription{}, f.getErr
	}
	return Subscription{ID: id, OrgID: orgID, URL: "https://example.test/hooks", Events: []string{EventTicketCreated}}, nil
}

func (f *fakeWebhookService) Create(_ context.Context, orgID uuid.UUID, req CreateSubscriptionRequest) (Subscription, error) {
	f.lastOrgID = orgID
	if f.createErr != nil {
		return Subscription{}, f.createErr
	}
	if len(req.Events) == 0 {
		return Subscription{}, ErrInvalidEvents
	}
	id := uuid.New()
	return Subscription{ID: id, OrgID: orgID, URL: req.URL, Events: req.Events, Secret: "whsec_test_secret", IsActive: true}, nil
}

func (f *fakeWebhookService) Update(_ context.Context, orgID, id uuid.UUID, req UpdateSubscriptionRequest) (Subscription, error) {
	f.lastOrgID = orgID
	f.lastSubscriptionID = id
	if f.updateErr != nil {
		return Subscription{}, f.updateErr
	}
	return Subscription{ID: id, OrgID: orgID, URL: "https://example.test/hooks", Events: []string{EventTicketCreated}, IsActive: true}, nil
}

func (f *fakeWebhookService) Delete(_ context.Context, orgID, id uuid.UUID) error {
	f.lastOrgID = orgID
	f.lastSubscriptionID = id
	return f.deleteErr
}

func (f *fakeWebhookService) ListDeliveries(_ context.Context, orgID, subscriptionID uuid.UUID, limit int, cursor string) ([]Delivery, string, error) {
	f.lastOrgID = orgID
	f.lastSubscriptionID = subscriptionID
	if f.listDeliveriesErr != nil {
		return nil, "", f.listDeliveriesErr
	}
	return f.deliveries, "", nil
}

func (f *fakeWebhookService) Test(_ context.Context, orgID, id uuid.UUID) (Delivery, error) {
	f.lastOrgID = orgID
	f.lastSubscriptionID = id
	if f.testErr != nil {
		return Delivery{}, f.testErr
	}
	return Delivery{ID: uuid.New(), SubscriptionID: id, EventType: EventPing, Status: StatusSent}, nil
}

func (f *fakeWebhookService) Redeliver(_ context.Context, orgID, subscriptionID, deliveryID uuid.UUID) (Delivery, error) {
	f.lastOrgID = orgID
	f.lastSubscriptionID = subscriptionID
	f.lastDeliveryID = deliveryID
	if f.redeliverErr != nil {
		return Delivery{}, f.redeliverErr
	}
	return Delivery{ID: deliveryID, SubscriptionID: subscriptionID, Status: StatusSent}, nil
}

func principalRequest(t *testing.T, method, target string, body []byte, orgID uuid.UUID, role string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req.WithContext(requestctx.WithPrincipal(req.Context(), requestctx.Principal{
		Kind:    requestctx.KindUser,
		ActorID: uuid.New(),
		OrgID:   orgID,
		Role:    role,
	}))
}

func setupHandler(t *testing.T, svc webhookService) *httptest.Server {
	t.Helper()
	h := NewHandler(svc)
	r := chi.NewRouter()
	r.Route("/v1/webhooks", func(rc chi.Router) {
		rc.Get("/", h.List)
		rc.With(auth.RequireRole("owner", "admin")).Post("/", h.Create)
		rc.Get("/{id}", h.Get)
		rc.With(auth.RequireRole("owner", "admin")).Patch("/{id}", h.Update)
		rc.With(auth.RequireRole("owner", "admin")).Delete("/{id}", h.Delete)
		rc.With(auth.RequireRole("owner", "admin")).Post("/{id}/test", h.Test)
		rc.Get("/{id}/deliveries", h.ListDeliveries)
		rc.With(auth.RequireRole("owner", "admin")).Post("/{id}/deliveries/{deliveryId}/redeliver", h.Redeliver)
	})
	return httptest.NewServer(r)
}

func decodeErrorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode error %q: %v", rec.Body.String(), err)
	}
	return envelope.Error.Code
}

func TestHandlerList(t *testing.T) {
	orgID := uuid.New()
	svc := &fakeWebhookService{subs: map[uuid.UUID]Subscription{}}
	sub, err := svc.Create(context.Background(), orgID, CreateSubscriptionRequest{URL: "https://example.test/hooks", Events: []string{EventTicketCreated}})
	if err != nil {
		t.Fatal(err)
	}
	svc.subs[sub.ID] = sub

	srv := setupHandler(t, svc)
	defer srv.Close()

	req := principalRequest(t, http.MethodGet, "/v1/webhooks", nil, orgID, "agent")
	rec := httptest.NewRecorder()
	srv.Config.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp SubscriptionListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Webhooks) != 1 || resp.Webhooks[0].ID != sub.ID {
		t.Fatalf("webhooks: %+v", resp.Webhooks)
	}
	if resp.Webhooks[0].Secret != "" {
		t.Fatalf("list leaked secret: %q", resp.Webhooks[0].Secret)
	}
}

func TestHandlerCreateAndGet(t *testing.T) {
	orgID := uuid.New()
	svc := &fakeWebhookService{subs: map[uuid.UUID]Subscription{}}
	srv := setupHandler(t, svc)
	defer srv.Close()

	body := `{"url":"https://example.test/hooks","events":["ticket.created","comment.added"]}`
	req := principalRequest(t, http.MethodPost, "/v1/webhooks", []byte(body), orgID, "admin")
	rec := httptest.NewRecorder()
	srv.Config.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var dto SubscriptionDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if dto.URL != "https://example.test/hooks" || dto.Secret != "whsec_test_secret" {
		t.Fatalf("dto: %+v", dto)
	}

	req = principalRequest(t, http.MethodGet, "/v1/webhooks/"+dto.ID.String(), nil, orgID, "agent")
	rec = httptest.NewRecorder()
	srv.Config.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("get status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got SubscriptionDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != dto.ID || got.Secret != "" {
		t.Fatalf("get leaked secret or wrong id: %+v", got)
	}
}

func TestHandlerCreateUnauthorizedAndInvalid(t *testing.T) {
	orgID := uuid.New()
	svc := &fakeWebhookService{}
	srv := setupHandler(t, svc)
	defer srv.Close()

	// Agent cannot create.
	req := principalRequest(t, http.MethodPost, "/v1/webhooks", []byte(`{"url":"https://example.test/hooks","events":["ticket.created"]}`), orgID, "agent")
	rec := httptest.NewRecorder()
	srv.Config.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("agent create status=%d", rec.Code)
	}

	// Invalid events.
	req = principalRequest(t, http.MethodPost, "/v1/webhooks", []byte(`{"url":"https://example.test/hooks","events":[]}`), orgID, "admin")
	rec = httptest.NewRecorder()
	srv.Config.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid events status=%d body=%s", rec.Code, rec.Body.String())
	}
	if decodeErrorCode(t, rec) != "invalid_events" {
		t.Fatalf("code=%q", decodeErrorCode(t, rec))
	}

	// Malformed JSON.
	req = principalRequest(t, http.MethodPost, "/v1/webhooks", []byte(`{`), orgID, "admin")
	rec = httptest.NewRecorder()
	srv.Config.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed status=%d", rec.Code)
	}
}

func TestHandlerGetNotFound(t *testing.T) {
	orgID := uuid.New()
	svc := &fakeWebhookService{getErr: ErrWebhookNotFound}
	srv := setupHandler(t, svc)
	defer srv.Close()

	req := principalRequest(t, http.MethodGet, "/v1/webhooks/"+uuid.NewString(), nil, orgID, "agent")
	rec := httptest.NewRecorder()
	srv.Config.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("not found status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandlerUpdate(t *testing.T) {
	orgID := uuid.New()
	id := uuid.New()
	svc := &fakeWebhookService{}
	srv := setupHandler(t, svc)
	defer srv.Close()

	req := principalRequest(t, http.MethodPatch, "/v1/webhooks/"+id.String(),
		[]byte(`{"is_active":true}`), orgID, "admin")
	rec := httptest.NewRecorder()
	srv.Config.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if svc.lastOrgID != orgID || svc.lastSubscriptionID != id {
		t.Fatalf("org=%s sub=%s", svc.lastOrgID, svc.lastSubscriptionID)
	}
}

func TestHandlerUpdateRejectsAgent(t *testing.T) {
	orgID := uuid.New()
	svc := &fakeWebhookService{}
	srv := setupHandler(t, svc)
	defer srv.Close()

	req := principalRequest(t, http.MethodPatch, "/v1/webhooks/"+uuid.NewString(),
		[]byte(`{}`), orgID, "agent")
	rec := httptest.NewRecorder()
	srv.Config.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("agent patch status=%d", rec.Code)
	}
}

func TestHandlerDelete(t *testing.T) {
	orgID := uuid.New()
	id := uuid.New()
	svc := &fakeWebhookService{}
	srv := setupHandler(t, svc)
	defer srv.Close()

	req := principalRequest(t, http.MethodDelete, "/v1/webhooks/"+id.String(), nil, orgID, "owner")
	rec := httptest.NewRecorder()
	srv.Config.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandlerDeleteNotFound(t *testing.T) {
	orgID := uuid.New()
	svc := &fakeWebhookService{deleteErr: ErrWebhookNotFound}
	srv := setupHandler(t, svc)
	defer srv.Close()

	req := principalRequest(t, http.MethodDelete, "/v1/webhooks/"+uuid.NewString(), nil, orgID, "owner")
	rec := httptest.NewRecorder()
	srv.Config.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandlerTest(t *testing.T) {
	orgID := uuid.New()
	id := uuid.New()
	svc := &fakeWebhookService{}
	srv := setupHandler(t, svc)
	defer srv.Close()

	req := principalRequest(t, http.MethodPost, "/v1/webhooks/"+id.String()+"/test", nil, orgID, "admin")
	rec := httptest.NewRecorder()
	srv.Config.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var dto DeliveryDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if dto.EventType != EventPing || dto.Status != StatusSent {
		t.Fatalf("dto: %+v", dto)
	}
}

func TestHandlerTestInactive(t *testing.T) {
	orgID := uuid.New()
	svc := &fakeWebhookService{testErr: ErrInactive}
	srv := setupHandler(t, svc)
	defer srv.Close()

	req := principalRequest(t, http.MethodPost, "/v1/webhooks/"+uuid.NewString()+"/test", nil, orgID, "admin")
	rec := httptest.NewRecorder()
	srv.Config.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("inactive status=%d body=%s", rec.Code, rec.Body.String())
	}
	if decodeErrorCode(t, rec) != "webhook_inactive" {
		t.Fatalf("code=%q", decodeErrorCode(t, rec))
	}
}

func TestHandlerListDeliveries(t *testing.T) {
	orgID := uuid.New()
	subID := uuid.New()
	deliveryID := uuid.New()
	svc := &fakeWebhookService{
		deliveries: []Delivery{
			{ID: deliveryID, SubscriptionID: subID, EventType: EventTicketCreated, Status: StatusSent, CreatedAt: time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)},
		},
	}
	srv := setupHandler(t, svc)
	defer srv.Close()

	req := principalRequest(t, http.MethodGet, "/v1/webhooks/"+subID.String()+"/deliveries", nil, orgID, "agent")
	rec := httptest.NewRecorder()
	srv.Config.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp DeliveryListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Deliveries) != 1 || resp.Deliveries[0].ID != deliveryID {
		t.Fatalf("deliveries: %+v", resp.Deliveries)
	}
}

func TestHandlerRedeliver(t *testing.T) {
	orgID := uuid.New()
	subID := uuid.New()
	deliveryID := uuid.New()
	svc := &fakeWebhookService{}
	srv := setupHandler(t, svc)
	defer srv.Close()

	req := principalRequest(t, http.MethodPost, "/v1/webhooks/"+subID.String()+"/deliveries/"+deliveryID.String()+"/redeliver", nil, orgID, "admin")
	rec := httptest.NewRecorder()
	srv.Config.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if svc.lastDeliveryID != deliveryID || svc.lastSubscriptionID != subID {
		t.Fatalf("delivery=%s sub=%s", svc.lastDeliveryID, svc.lastSubscriptionID)
	}
}

func TestHandlerRedeliverNotFound(t *testing.T) {
	orgID := uuid.New()
	svc := &fakeWebhookService{redeliverErr: ErrDeliveryNotFound}
	srv := setupHandler(t, svc)
	defer srv.Close()

	req := principalRequest(t, http.MethodPost, "/v1/webhooks/"+uuid.NewString()+"/deliveries/"+uuid.NewString()+"/redeliver", nil, orgID, "admin")
	rec := httptest.NewRecorder()
	srv.Config.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandlerInvalidID(t *testing.T) {
	orgID := uuid.New()
	svc := &fakeWebhookService{}
	srv := setupHandler(t, svc)
	defer srv.Close()

	cases := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/v1/webhooks/not-a-uuid"},
		{http.MethodPost, "/v1/webhooks/not-a-uuid/test"},
		{http.MethodGet, "/v1/webhooks/not-a-uuid/deliveries"},
		{http.MethodPost, "/v1/webhooks/sub/deliveries/not-a-uuid/redeliver"},
		{http.MethodPatch, "/v1/webhooks/not-a-uuid"},
		{http.MethodDelete, "/v1/webhooks/not-a-uuid"},
	}
	for _, tc := range cases {
		req := principalRequest(t, tc.method, tc.path, nil, orgID, "admin")
		rec := httptest.NewRecorder()
		srv.Config.Handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s %s status=%d body=%s", tc.method, tc.path, rec.Code, rec.Body.String())
		}
	}
}

func TestHandlerErrorsPropagate(t *testing.T) {
	orgID := uuid.New()
	svc := &fakeWebhookService{listErr: errors.New("db down")}
	srv := setupHandler(t, svc)
	defer srv.Close()

	req := principalRequest(t, http.MethodGet, "/v1/webhooks", nil, orgID, "agent")
	rec := httptest.NewRecorder()
	srv.Config.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if envelope.Error.Code == "" {
		t.Fatal("missing error code")
	}
}
