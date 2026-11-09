package attachments

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/HenriNorman/gregport/internal/platform/requestctx"
)

func handlerFixture(t *testing.T) (*Handler, *fakeRepo, *fakeStore, uuid.UUID) {
	t.Helper()
	repo := newFakeRepo()
	store := newFakeStore()
	orgID := uuid.New()
	repo.seedTicket(orgID, 1042)
	svc := NewService(repo, store, 10<<20, 5*time.Minute, 2*time.Minute, time.Hour)
	return NewHandler(svc), repo, store, orgID
}

func withPrincipal(r *http.Request, orgID uuid.UUID) *http.Request {
	ctx := requestctx.WithPrincipal(r.Context(), requestctx.Principal{
		Kind: requestctx.KindUser, ActorID: uuid.New(), OrgID: orgID, Role: "agent",
	})
	return r.WithContext(ctx)
}

func TestHandlerPresignCreated(t *testing.T) {
	h, _, _, orgID := handlerFixture(t)
	body := `{"filename":"a.pdf","mime_type":"application/pdf","size":10}`
	req := withPrincipal(httptest.NewRequest(http.MethodPost, "/v1/tickets/1042/attachments/presign", strings.NewReader(body)), orgID)
	rec := httptest.NewRecorder()
	r := chi.NewRouter()
	r.Post("/v1/tickets/{number}/attachments/presign", h.Presign)
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out PresignResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.UploadURL == "" {
		t.Fatal("missing upload url")
	}
}

func TestHandlerPresignOversize(t *testing.T) {
	h, _, _, orgID := handlerFixture(t)
	body := `{"filename":"a.pdf","mime_type":"application/pdf","size":52428800}`
	req := withPrincipal(httptest.NewRequest(http.MethodPost, "/v1/tickets/1042/attachments/presign", strings.NewReader(body)), orgID)
	rec := httptest.NewRecorder()
	r := chi.NewRouter()
	r.Post("/v1/tickets/{number}/attachments/presign", h.Presign)
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandlerPresignInvalidNumber(t *testing.T) {
	h, _, _, orgID := handlerFixture(t)
	req := withPrincipal(httptest.NewRequest(http.MethodPost, "/v1/tickets/abc/attachments/presign", strings.NewReader(`{}`)), orgID)
	rec := httptest.NewRecorder()
	r := chi.NewRouter()
	r.Post("/v1/tickets/{number}/attachments/presign", h.Presign)
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandlerConfirmMissingObject(t *testing.T) {
	h, repo, _, orgID := handlerFixture(t)
	svcPresign := NewService(repo, newFakeStore(), 10<<20, time.Minute, time.Minute, time.Hour)
	res, _ := svcPresign.Presign(context.Background(), orgID, 1042, PresignRequest{
		Filename: "a.pdf", MIMEType: "application/pdf", Size: 10,
	})
	req := withPrincipal(httptest.NewRequest(http.MethodPost, "/v1/attachments/"+res.AttachmentID+"/confirm", nil), orgID)
	rec := httptest.NewRecorder()
	r := chi.NewRouter()
	r.Post("/v1/attachments/{id}/confirm", h.Confirm)
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandlerDownloadForeign(t *testing.T) {
	h, repo, store, orgID := handlerFixture(t)
	svc := NewService(repo, store, 10<<20, time.Minute, time.Minute, time.Hour)
	res, _ := svc.Presign(context.Background(), orgID, 1042, PresignRequest{
		Filename: "a.pdf", MIMEType: "application/pdf", Size: 4,
	})
	id, _ := uuid.Parse(res.AttachmentID)
	store.putObject(repo.attachments[id].StorageKey, []byte("PDF!"), "application/pdf")
	if _, err := svc.Confirm(context.Background(), orgID, id); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	req := withPrincipal(httptest.NewRequest(http.MethodGet, "/v1/attachments/"+res.AttachmentID+"/download", nil), uuid.New())
	rec := httptest.NewRecorder()
	r := chi.NewRouter()
	r.Get("/v1/attachments/{id}/download", h.Download)
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}
