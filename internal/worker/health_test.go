package worker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakePinger struct{ err error }

func (f fakePinger) Ping(context.Context) error { return f.err }

func TestHealthHandlerLive(t *testing.T) {
	h := HealthHandler(fakePinger{}, fakePinger{})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["status"] != "ok" {
		t.Fatalf("body: %v", body)
	}
}

func TestHealthHandlerReady(t *testing.T) {
	tests := []struct {
		name      string
		dbErr     error
		redisErr  error
		wantCode  int
		wantState string
	}{
		{name: "both healthy", wantCode: http.StatusOK, wantState: "ok"},
		{name: "db down", dbErr: errors.New("db"), wantCode: http.StatusServiceUnavailable, wantState: "unready"},
		{name: "redis down", redisErr: errors.New("redis"), wantCode: http.StatusServiceUnavailable, wantState: "unready"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := HealthHandler(fakePinger{err: tt.dbErr}, fakePinger{err: tt.redisErr})

			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

			if rec.Code != tt.wantCode {
				t.Fatalf("status: %d, want %d", rec.Code, tt.wantCode)
			}
			var body map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if body["status"] != tt.wantState {
				t.Fatalf("body: %v", body)
			}
		})
	}
}
