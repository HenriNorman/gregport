package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/HenriNorman/gregport/internal/platform/requestctx"
)

func TestRequestID_GeneratesWhenMissing(t *testing.T) {
	var seen string
	h := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = requestctx.RequestID(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if seen == "" {
		t.Fatal("expected generated request id in context")
	}
	if got := rec.Header().Get("X-Request-ID"); got != seen {
		t.Fatalf("header %q != context %q", got, seen)
	}
}

func TestRequestID_EchoesValidValue(t *testing.T) {
	var seen string
	h := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = requestctx.RequestID(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-ID", "client-req-123")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if seen != "client-req-123" {
		t.Fatalf("context id: got %q", seen)
	}
}

func TestRequestID_RejectsUnsafeValue(t *testing.T) {
	tests := []string{
		"has space",
		"line\nbreak",
		"<script>",
		string(make([]byte, 129)),
	}

	for _, value := range tests {
		t.Run(value, func(t *testing.T) {
			var seen string
			h := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen = requestctx.RequestID(r.Context())
				w.WriteHeader(http.StatusNoContent)
			}))

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set("X-Request-ID", value)
			h.ServeHTTP(httptest.NewRecorder(), req)

			if seen == "" || seen == value {
				t.Fatalf("unsafe id was not replaced: %q", seen)
			}
			if len(seen) > 128 {
				t.Fatalf("generated id too long: %d", len(seen))
			}
		})
	}
}
