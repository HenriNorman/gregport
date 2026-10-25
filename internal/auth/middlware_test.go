package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/HenriNorman/gregport/internal/platform/requestctx"
)

func TestAuthenticate_NoHeader_401(t *testing.T) {
	h := Authenticate(testTokenManager())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not run")
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestAuthenticate_MalformedHeader_401(t *testing.T) {
	cases := []string{"Bearer", "Basic abc", "Bearer ", "bearer"}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			h := Authenticate(testTokenManager())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Fatal("handler should not run")
			}))
			req := httptest.NewRequest("GET", "/", nil)
			req.Header.Set("Authorization", c)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("%q: expected 401, got %d", c, rec.Code)
			}
		})
	}
}

func TestAuthenticate_InvalidToken_401(t *testing.T) {
	h := Authenticate(testTokenManager())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not run")
	}))
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer not-a-jwt")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestAuthenticate_ExpiredToken_401(t *testing.T) {
	expired := NewTokenManager("0123456789abcdef0123456789abcdef", -1*time.Minute, 30*24*time.Hour)
	tok, _, _ := expired.IssueAccess(uuid.New(), uuid.New(), "owner")

	h := Authenticate(testTokenManager())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not run")
	}))
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestAuthenticate_ValidToken_SetsPrincipal(t *testing.T) {
	tm := testTokenManager()
	uid := uuid.New()
	oid := uuid.New()
	tok, _, _ := tm.IssueAccess(uid, oid, "owner")

	var got requestctx.Principal
	var gotOK bool

	h := Authenticate(tm)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, gotOK = requestctx.FromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !gotOK {
		t.Fatal("principal missing from context")
	}
	if got.UserID != uid || got.OrgID != oid || got.Role != "owner" {
		t.Fatalf("principal mismatch: %+v", got)
	}
}

func TestRequireRole_Allowed_Passes(t *testing.T) {
	h := RequireRole("owner", "admin")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/", nil)
	ctx := requestctx.WithPrincipal(req.Context(), requestctx.Principal{Role: "admin"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req.WithContext(ctx))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestRequireRole_Denied_403(t *testing.T) {
	h := RequireRole("owner")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not run")
	}))

	req := httptest.NewRequest("GET", "/", nil)
	ctx := requestctx.WithPrincipal(req.Context(), requestctx.Principal{Role: "agent"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req.WithContext(ctx))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestRequireRole_NoPrincipal_401(t *testing.T) {
	h := RequireRole("owner")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not run")
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}
