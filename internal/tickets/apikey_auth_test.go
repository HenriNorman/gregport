package tickets

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/HenriNorman/gregport/internal/platform/requestctx"
)

func seedValidKey(t *testing.T, repo *Repository, orgID, userID uuid.UUID, raw string) APIKey {
	t.Helper()
	prefix, ok := parseAPIKeyPrefix(raw)
	if !ok {
		t.Fatalf("invalid raw key %q", raw)
	}
	k, err := repo.CreateAPIKey(context.Background(), APIKey{
		OrgID:           orgID,
		Name:            "test",
		Prefix:          prefix,
		KeyHash:         HashAPIKey(raw),
		CreatedBy:       userID,
		RateLimitPerMin: 600,
	})
	if err != nil {
		t.Fatalf("seed key: %v", err)
	}
	return k
}

func TestAuthenticateAPIKey_Valid(t *testing.T) {
	
	_, repo := newTestHandler(t)
	orgA, _, _ := setupTwoOrgs(t, repo)
	ownerID := seedMembership(t, repo, orgA)

	raw := "gp_live_" + uuid.NewString()[:8] + "_" + uuid.NewString()
	k := seedValidKey(t, repo, orgA, ownerID, raw)

	svc := NewService(repo, NewInvitationTokenManager(7*24*time.Hour))
	p, err := svc.AuthenticateAPIKey(context.Background(), raw)
	if err != nil {
		t.Fatalf("auth: %v", err)
	}
	if p.Kind != requestctx.KindAPIKey {
		t.Fatalf("expected KindAPIKey, got %s", p.Kind)
	}
	if p.ActorID != k.ID || p.OrgID != orgA || p.Role != "agent" {
		t.Fatalf("principal mismatch: %+v", p)
	}
}

func TestAuthenticateAPIKey_Revoked_401(t *testing.T) {
	_, repo := newTestHandler(t)
	orgA, _, _ := setupTwoOrgs(t, repo)
	ownerID := seedMembership(t, repo, orgA)

	raw := "gp_live_" + uuid.NewString()[:8] + "_" + uuid.NewString()
	k := seedValidKey(t, repo, orgA, ownerID, raw)
	if err := repo.RevokeAPIKey(context.Background(), orgA, k.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	svc := NewService(repo, NewInvitationTokenManager(7*24*time.Hour))
	_, err := svc.AuthenticateAPIKey(context.Background(), raw)
	if err != ErrAPIKeyNotFound {
		t.Fatalf("expected ErrAPIKeyNotFound, got %v", err)
	}
}

func TestAuthenticateAPIKey_Expired_401(t *testing.T) {
	_, repo := newTestHandler(t)
	orgA, _, _ := setupTwoOrgs(t, repo)
	ownerID := seedMembership(t, repo, orgA)
	ctx := context.Background()

	raw := "gp_live_" + uuid.NewString()[:8] + "_" + uuid.NewString()
	prefix, _ := parseAPIKeyPrefix(raw)
	past := time.Now().Add(-time.Hour)
	k, err := repo.CreateAPIKey(ctx, APIKey{
		OrgID:           orgA,
		Name:            "expired",
		Prefix:          prefix,
		KeyHash:         HashAPIKey(raw),
		CreatedBy:       ownerID,
		RateLimitPerMin: 600,
		ExpiresAt:       &past,
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	_ = k

	svc := NewService(repo, NewInvitationTokenManager(7*24*time.Hour))
	_, err = svc.AuthenticateAPIKey(ctx, raw)
	if err != ErrAPIKeyNotFound {
		t.Fatalf("expected ErrAPIKeyNotFound, got %v", err)
	}
}

func TestAuthenticateAPIKey_WrongSecret_401(t *testing.T) {
	_, repo := newTestHandler(t)
	orgA, _, _ := setupTwoOrgs(t, repo)
	ownerID := seedMembership(t, repo, orgA)
	idPart := uuid.NewString()[:8]

	raw := "gp_live_" + idPart + "_" + uuid.NewString()
	seedValidKey(t, repo, orgA, ownerID, raw)

	// same prefix, wrong secret
	bad := "gp_live_" + idPart + "_wrongsecret"


	svc := NewService(repo, NewInvitationTokenManager(7*24*time.Hour))
	_, err := svc.AuthenticateAPIKey(context.Background(), bad)
	if err != ErrAPIKeyNotFound {
		t.Fatalf("expected ErrAPIKeyNotFound, got %v", err)
	}
}

func TestAuthenticateAPIKey_UnknownPrefix_401(t *testing.T) {
	_, repo := newTestHandler(t)

	svc := NewService(repo, NewInvitationTokenManager(7*24*time.Hour))
	_, err := svc.AuthenticateAPIKey(context.Background(), "gp_live_unknown_secret")
	if err != ErrAPIKeyNotFound {
		t.Fatalf("expected ErrAPIKeyNotFound, got %v", err)
	}
}

func TestAuthenticateAPIKey_LastUsedThrottled(t *testing.T) {
	_, repo := newTestHandler(t)
	orgA, _, _ := setupTwoOrgs(t, repo)
	ownerID := seedMembership(t, repo, orgA)

	raw := "gp_live_" + uuid.NewString()[:8] + "_" + uuid.NewString()
	k := seedValidKey(t, repo, orgA, ownerID, raw)

	svc := NewService(repo, NewInvitationTokenManager(7*24*time.Hour))
	ctx := context.Background()

	// first auth should schedule a last_used_at update
	if _, err := svc.AuthenticateAPIKey(ctx, raw); err != nil {
		t.Fatalf("auth 1: %v", err)
	}

	// wait for the background goroutine to write
	deadline := time.Now().Add(2 * time.Second)
	var firstUsed *time.Time
	for time.Now().Before(deadline) {
		_ = repo.pool.QueryRow(ctx,
			`SELECT last_used_at FROM api_keys WHERE id = $1`, k.ID,
		).Scan(&firstUsed)
		if firstUsed != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if firstUsed == nil {
		t.Fatal("last_used_at was not written after first auth")
	}

	// immediate second auth should not write again (within the 1m window)
	if _, err := svc.AuthenticateAPIKey(ctx, raw); err != nil {
		t.Fatalf("auth 2: %v", err)
	}
	time.Sleep(150 * time.Millisecond)

	var secondUsed time.Time
	if err := repo.pool.QueryRow(ctx,
		`SELECT last_used_at FROM api_keys WHERE id = $1`, k.ID,
	).Scan(&secondUsed); err != nil {
		t.Fatalf("read last_used_at: %v", err)
	}
	if !secondUsed.Equal(*firstUsed) {
		t.Fatalf("last_used_at changed on second auth within throttle window: %v -> %v",
			*firstUsed, secondUsed)
	}
}

func TestAuthenticateAPIKey_CanCreateTicket_NotAPIKey(t *testing.T) {
	h, repo := newTestHandler(t)
	orgA, _, _ := setupTwoOrgs(t, repo)
	ownerID := seedMembership(t, repo, orgA)

	raw := "gp_live_" + uuid.NewString()[:8] + "_" + uuid.NewString()
	k := seedValidKey(t, repo, orgA, ownerID, raw)

	// build a router with a handler that simulates API-key-authenticated requests
	// by injecting the principal via context (the real middleware does this in
	// production; here we bypass the JWT step and set the principal directly).
	apiKeyPrincipal := func() requestctx.Principal {
		return requestctx.Principal{
			Kind:    requestctx.KindAPIKey,
			ActorID: k.ID,
			OrgID:   orgA,
			Role:    "agent",
		}
	}

	r := chi.NewRouter()
	r.Post("/tickets", h.Create)
	r.Post("/api-keys", h.CreateAPIKey)

	// API key can create a ticket
	body, _ := json.Marshal(CreateTicketRequest{
		RequesterEmail: "cust@test.local",
		RequesterName:  "Cust",
		Subject:        "from api key",
		Body:           "hi",
		Priority:       "normal",
	})
	req := httptest.NewRequest("POST", "/tickets", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(requestctx.WithPrincipal(req.Context(), apiKeyPrincipal()))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("api key create ticket: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	// API key cannot create an API key
	body2, _ := json.Marshal(CreateAPIKeyRequest{Name: "nope"})
	req2 := httptest.NewRequest("POST", "/api-keys", bytes.NewReader(body2))
	req2.Header.Set("Content-Type", "application/json")
	req2 = req2.WithContext(requestctx.WithPrincipal(req2.Context(), apiKeyPrincipal()))
	rec2 := httptest.NewRecorder()
	r.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusForbidden {
		t.Fatalf("api key create key: expected 403, got %d: %s", rec2.Code, rec2.Body.String())
	}
}
