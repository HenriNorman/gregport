package inboundemail

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

type processFunc func(context.Context, []byte, string, string) error

type adapterService struct {
	fn processFunc
}

func (a *adapterService) Process(ctx context.Context, rawBody []byte, timestamp, signature string) error {
	return a.fn(ctx, rawBody, timestamp, signature)
}

func TestHandlerSubmitAccepted(t *testing.T) {
	svc := &adapterService{fn: func(context.Context, []byte, string, string) error { return nil }}
	h := NewHandler(svc)
	req := httptest.NewRequest(http.MethodPost, "/v1/inbound/email", strings.NewReader("raw body"))
	req.Header.Set(HeaderTimestamp, "1700000000")
	req.Header.Set(HeaderSignature, "sigsig")
	rec := httptest.NewRecorder()
	h.Submit(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d want 202", rec.Code)
	}
}

func TestHandlerSubmitBadSignature(t *testing.T) {
	svc := &adapterService{fn: func(context.Context, []byte, string, string) error { return ErrInvalidSignature }}
	h := NewHandler(svc)
	req := httptest.NewRequest(http.MethodPost, "/v1/inbound/email", strings.NewReader("raw body"))
	req.Header.Set(HeaderTimestamp, "1700000000")
	req.Header.Set(HeaderSignature, "sigsig")
	rec := httptest.NewRecorder()
	h.Submit(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401", rec.Code)
	}
}

func TestHandlerSubmitExpired(t *testing.T) {
	svc := &adapterService{fn: func(context.Context, []byte, string, string) error { return ErrExpiredSignature }}
	h := NewHandler(svc)
	req := httptest.NewRequest(http.MethodPost, "/v1/inbound/email", strings.NewReader("raw body"))
	req.Header.Set(HeaderTimestamp, "1700000000")
	req.Header.Set(HeaderSignature, "sigsig")
	rec := httptest.NewRecorder()
	h.Submit(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401", rec.Code)
	}
}

func TestHandlerSubmitUnroutable(t *testing.T) {
	svc := &adapterService{fn: func(context.Context, []byte, string, string) error { return ErrOrgNotFound }}
	h := NewHandler(svc)
	req := httptest.NewRequest(http.MethodPost, "/v1/inbound/email", strings.NewReader("raw body"))
	req.Header.Set(HeaderTimestamp, "1700000000")
	req.Header.Set(HeaderSignature, "sigsig")
	rec := httptest.NewRecorder()
	h.Submit(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d want 422", rec.Code)
	}
}

func TestHandlerSubmitInternalError(t *testing.T) {
	svc := &adapterService{fn: func(context.Context, []byte, string, string) error { return errors.New("boom") }}
	h := NewHandler(svc)
	req := httptest.NewRequest(http.MethodPost, "/v1/inbound/email", strings.NewReader("raw body"))
	req.Header.Set(HeaderTimestamp, "1700000000")
	req.Header.Set(HeaderSignature, "sigsig")
	rec := httptest.NewRecorder()
	h.Submit(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500", rec.Code)
	}
}

func TestHandlerRoutesRegistersPath(t *testing.T) {
	svc := &adapterService{fn: func(context.Context, []byte, string, string) error { return nil }}
	h := NewHandler(svc)
	r := chi.NewRouter()
	h.Routes(r)
	req := httptest.NewRequest(http.MethodPost, "/v1/inbound/email", strings.NewReader("x"))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d want 202", rec.Code)
	}
}

var _ = io.Discard
