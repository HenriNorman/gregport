package tickets

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	
	"github.com/google/uuid"
	"github.com/go-chi/chi/v5"
)

func TestCreateAPIKey_ReturnsRawOnce_StoresHash(t *testing.T) {
	h, repo := newTestHandler(t)
	orgA, _, _ := setupTwoOrgs(t, repo)
	ownerID := seedMembership(t, repo, orgA)
	ctx := context.Background()

	r := chi.NewRouter()
	r.Post("/api-keys", h.CreateAPIKey)

	body, _ := json.Marshal(CreateAPIKeyRequest{Name: "CI"})
	req := httptest.NewRequest("POST", "/api-keys", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(inviteCtx(orgA, ownerID, "owner"))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp CreateAPIKeyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !strings.HasPrefix(resp.Key, "gp_live_") {
		t.Fatalf("expected key to start with gp_live_, got %q", resp.Key)
	}
	if resp.APIKey.Prefix == "" || !strings.HasPrefix(resp.Key, resp.APIKey.Prefix) {
		t.Fatalf("prefix %q must be a prefix of key %q", resp.APIKey.Prefix, resp.Key)
	}

	// raw key must not be stored
	var rawCount int
	_ = repo.pool.QueryRow(ctx,
		`SELECT count(*) FROM api_keys WHERE key_hash = $1 OR prefix = $1`, resp.Key,
	).Scan(&rawCount)
	if rawCount != 0 {
		t.Fatal("raw key must not be stored in api_keys")
	}

	// hash must be the SHA-256 of the full key
	var storedHash string
	_ = repo.pool.QueryRow(ctx,
		`SELECT key_hash FROM api_keys WHERE org_id = $1 AND name = 'CI'`, orgA,
	).Scan(&storedHash)
	if storedHash != HashAPIKey(resp.Key) {
		t.Fatal("stored hash does not match SHA-256 of the full key")
	}
}

func TestCreateAPIKey_ListNeverReturnsRaw(t *testing.T) {
	h, repo := newTestHandler(t)
	orgA, _, _ := setupTwoOrgs(t, repo)
	ownerID := seedMembership(t, repo, orgA)
	ctx := context.Background()

	raw := "gp_live_" + uuid.NewString()[:8] + "_" + uuid.NewString()
	prefix, _ := parseAPIKeyPrefix(raw)
	if _, err := repo.CreateAPIKey(ctx, APIKey{
		OrgID:           orgA,
		Name:            "seeded-" + uuid.NewString()[:8],
		Prefix:          prefix,
		KeyHash:         HashAPIKey(raw),
		CreatedBy:       ownerID,
		RateLimitPerMin: 600,
	}); err != nil {
		t.Fatalf("seed key: %v", err)
	}

	r := chi.NewRouter()
	r.Get("/api-keys", h.ListAPIKeys)

	req := httptest.NewRequest("GET", "/api-keys", nil)
	req = req.WithContext(inviteCtx(orgA, ownerID, "owner"))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	// raw body must not contain the raw key or the hash
	body := rec.Body.String()
	if strings.Contains(body, raw) {
		t.Fatal("list response contains the raw key")
	}
	if strings.Contains(body, HashAPIKey(raw)) {
		t.Fatal("list response contains the key hash")
	}

	var resp APIKeyListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.APIKeys) != 1 {
		t.Fatalf("expected 1 key, got %d", len(resp.APIKeys))
	}
	if resp.APIKeys[0].Prefix != prefix {
		t.Fatalf("prefix mismatch: expected %s, got %s", prefix, resp.APIKeys[0].Prefix)
	}
}

func TestRevokeAPIKey_SetsRevokedAt(t *testing.T) {
	h, repo := newTestHandler(t)
	orgA, _, _ := setupTwoOrgs(t, repo)
	ownerID := seedMembership(t, repo, orgA)
	ctx := context.Background()

	raw := "gp_live_" + uuid.NewString()[:8] + "_" + uuid.NewString()
	prefix, _ := parseAPIKeyPrefix(raw)
	k, err := repo.CreateAPIKey(ctx, APIKey{
		OrgID:           orgA,
		Name:            "to-revoke-" + uuid.NewString()[:8],
		Prefix:          prefix,
		KeyHash:         HashAPIKey(raw),
		CreatedBy:       ownerID,
		RateLimitPerMin: 600,
	})
	if err != nil {
		t.Fatalf("seed key: %v", err)
	}

	r := chi.NewRouter()
	r.Delete("/api-keys/{id}", h.RevokeAPIKey)

	req := httptest.NewRequest("DELETE", "/api-keys/"+k.ID.String(), nil)
	req = req.WithContext(inviteCtx(orgA, ownerID, "owner"))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}

	var revokedAt *string
	_ = repo.pool.QueryRow(ctx,
		`SELECT revoked_at::text FROM api_keys WHERE id = $1`, k.ID,
	).Scan(&revokedAt)
	if revokedAt == nil {
		t.Fatal("expected revoked_at to be set")
	}
}

func TestCreateAPIKey_AuditRow(t *testing.T) {
	h, repo := newTestHandler(t)
	orgA, _, _ := setupTwoOrgs(t, repo)
	ownerID := seedMembership(t, repo, orgA)
	ctx := context.Background()

	r := chi.NewRouter()
	r.Post("/api-keys", h.CreateAPIKey)

	body, _ := json.Marshal(CreateAPIKeyRequest{Name: "audited"})
	req := httptest.NewRequest("POST", "/api-keys", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(inviteCtx(orgA, ownerID, "owner"))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var count int
	_ = repo.pool.QueryRow(ctx,
		`SELECT count(*) FROM audit_logs
		 WHERE org_id = $1 AND action = 'api_key.created'`, orgA,
	).Scan(&count)
	if count != 1 {
		t.Fatalf("expected 1 api_key.created audit row, got %d", count)
	}

	// revoke also audits
	var resp CreateAPIKeyResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)

	r2 := chi.NewRouter()
	r2.Delete("/api-keys/{id}", h.RevokeAPIKey)
	req2 := httptest.NewRequest("DELETE", "/api-keys/"+resp.APIKey.ID, nil)
	req2 = req2.WithContext(inviteCtx(orgA, ownerID, "owner"))
	rec2 := httptest.NewRecorder()
	r2.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusNoContent {
		t.Fatalf("expected 204 on revoke, got %d", rec2.Code)
	}
	_ = repo.pool.QueryRow(ctx,
		`SELECT count(*) FROM audit_logs
		 WHERE org_id = $1 AND action = 'api_key.revoked'`, orgA,
	).Scan(&count)
	if count != 1 {
		t.Fatalf("expected 1 api_key.revoked audit row, got %d", count)
	}
}

func TestCreateAPIKey_Agent_403(t *testing.T) {
	h, repo := newTestHandler(t)
	orgA, _, _ := setupTwoOrgs(t, repo)
	agentID := seedMembership(t, repo, orgA)

	r := chi.NewRouter()
	r.Post("/api-keys", h.CreateAPIKey)

	body, _ := json.Marshal(CreateAPIKeyRequest{Name: "nope"})
	req := httptest.NewRequest("POST", "/api-keys", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(inviteCtx(orgA, agentID, "agent"))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for agent, got %d: %s", rec.Code, rec.Body.String())
	}
}
