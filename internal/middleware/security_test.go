package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSecurityHeaders(t *testing.T) {
	h := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	for _, name := range []string{
		"X-Content-Type-Options", "X-Frame-Options", "Referrer-Policy",
		"Permissions-Policy", "Content-Security-Policy",
	} {
		if rec.Header().Get(name) == "" {
			t.Errorf("missing %s", name)
		}
	}
	if rec.Header().Get("Strict-Transport-Security") != "" {
		t.Error("HSTS must not be sent on plain HTTP")
	}
}

func TestCORS_AllowList(t *testing.T) {
	h := CORS(CORSConfig{AllowedOrigins: []string{"https://app.example.com"}})(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
	)

	allowed := httptest.NewRequest(http.MethodGet, "/", nil)
	allowed.Header.Set("Origin", "https://app.example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, allowed)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Fatalf("allowed origin not echoed: %q", got)
	}

	denied := httptest.NewRequest(http.MethodOptions, "/", nil)
	denied.Header.Set("Origin", "https://evil.example.com")
	denied.Header.Set("Access-Control-Request-Method", http.MethodPost)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, denied)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for a preflight from a disallowed origin, got %d", rec.Code)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("disallowed origin must not receive CORS headers")
	}
}

func TestBodyLimit_RejectsOversizedBody(t *testing.T) {
	h := BodyLimit(4)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.ReadAll(r.Body); err != nil {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", io.NopCloser(
		&repeatingReader{remaining: 10},
	))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d", rec.Code)
	}
}

type repeatingReader struct{ remaining int }

func (r *repeatingReader) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, io.EOF
	}
	n := len(p)
	if n > r.remaining {
		n = r.remaining
	}
	for i := 0; i < n; i++ {
		p[i] = 'a'
	}
	r.remaining -= n
	return n, nil
}
