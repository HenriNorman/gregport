package tickets

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func TestListMembers_AnyMember(t *testing.T) {
	h, repo := newTestHandler(t)
	orgA, _, _ := setupTwoOrgs(t, repo)
	viewerID := seedMembership(t, repo, orgA)
	seedMembership(t, repo, orgA)

	r := chi.NewRouter()
	r.Get("/members", h.ListMembers)

	req := httptest.NewRequest("GET", "/members", nil)
	req = req.WithContext(principalCtx(orgA, viewerID))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp MemberListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Members) < 2 {
		t.Fatalf("expected at least 2 members, got %d", len(resp.Members))
	}
}

func TestUpdateMemberRole_OwnerPromotesAgentToAdmin(t *testing.T) {
	h, repo := newTestHandler(t)
	orgA, _, _ := setupTwoOrgs(t, repo)
	ownerID := seedMembership(t, repo, orgA)
	targetID := seedMembership(t, repo, orgA)

	r := chi.NewRouter()
	r.Patch("/members/{userId}", h.UpdateMemberRole)

	body, _ := json.Marshal(UpdateMemberRequest{Role: "admin"})
	req := httptest.NewRequest("PATCH", "/members/"+targetID.String(), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(inviteCtx(orgA, ownerID, "owner"))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}

	var role string
	if err := repo.pool.QueryRow(context.Background(),
		`SELECT role FROM memberships WHERE org_id = $1 AND user_id = $2`,
		orgA, targetID).Scan(&role); err != nil {
		t.Fatalf("query role: %v", err)
	}
	if role != "admin" {
		t.Fatalf("expected admin, got %s", role)
	}
}

func TestUpdateMemberRole_AdminPromotesToAdmin_403(t *testing.T) {
	h, repo := newTestHandler(t)
	orgA, _, _ := setupTwoOrgs(t, repo)
	adminID := seedMembership(t, repo, orgA)
	targetID := seedMembership(t, repo, orgA)

	r := chi.NewRouter()
	r.Patch("/members/{userId}", h.UpdateMemberRole)

	body, _ := json.Marshal(UpdateMemberRequest{Role: "admin"})
	req := httptest.NewRequest("PATCH", "/members/"+targetID.String(), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(inviteCtx(orgA, adminID, "admin"))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestUpdateMemberRole_GrantOwner_403(t *testing.T) {
	h, repo := newTestHandler(t)
	orgA, _, _ := setupTwoOrgs(t, repo)
	ownerID := seedMembership(t, repo, orgA)
	targetID := seedMembership(t, repo, orgA)

	r := chi.NewRouter()
	r.Patch("/members/{userId}", h.UpdateMemberRole)

	body, _ := json.Marshal(UpdateMemberRequest{Role: "owner"})
	req := httptest.NewRequest("PATCH", "/members/"+targetID.String(), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(inviteCtx(orgA, ownerID, "owner"))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for granting owner, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRemoveMember_LastOwner_422(t *testing.T) {
	h, repo := newTestHandler(t)
	orgA, _, _ := setupTwoOrgs(t, repo)
	ownerID := seedMembership(t, repo, orgA)

	// promote so they are the only owner
	if err := repo.UpdateMemberRole(context.Background(), orgA, ownerID, "owner"); err != nil {
		t.Fatalf("promote: %v", err)
	}
	// principal is a different admin, so the removal is not self-referential
	adminID := seedMembership(t, repo, orgA)

	r := chi.NewRouter()
	r.Delete("/members/{userId}", h.RemoveMember)

	req := httptest.NewRequest("DELETE", "/members/"+ownerID.String(), nil)
	req = req.WithContext(inviteCtx(orgA, adminID, "admin"))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for last owner, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRemoveMember_ConcurrentLastTwoOwners_OneSurvives(t *testing.T) {
	h, repo := newTestHandler(t)
	orgA, _, _ := setupTwoOrgs(t, repo)
	ctx := context.Background()

	owner1 := seedMembership(t, repo, orgA)
	owner2 := seedMembership(t, repo, orgA)
	if err := repo.UpdateMemberRole(ctx, orgA, owner1, "owner"); err != nil {
		t.Fatalf("promote 1: %v", err)
	}
	if err := repo.UpdateMemberRole(ctx, orgA, owner2, "owner"); err != nil {
		t.Fatalf("promote 2: %v", err)
	}
	adminID := seedMembership(t, repo, orgA)

	r := chi.NewRouter()
	r.Delete("/members/{userId}", h.RemoveMember)

	var wg sync.WaitGroup
	codes := make([]int, 2)
	for i, target := range []uuid.UUID{owner1, owner2} {
		wg.Add(1)
		go func(i int, target uuid.UUID) {
			defer wg.Done()
			req := httptest.NewRequest("DELETE", "/members/"+target.String(), nil)
			req = req.WithContext(inviteCtx(orgA, adminID, "admin"))
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			codes[i] = rec.Code
		}(i, target)
	}
	wg.Wait()

	var ok, refused int
	for _, c := range codes {
		switch c {
		case http.StatusNoContent:
			ok++
		case http.StatusUnprocessableEntity:
			refused++
		default:
			t.Fatalf("unexpected status: %d", c)
		}
	}
	if ok != 1 || refused != 1 {
		t.Fatalf("expected one removal and one refusal, got ok=%d refused=%d", ok, refused)
	}

	var owners int
	if err := repo.pool.QueryRow(ctx,
		`SELECT count(*) FROM memberships WHERE org_id = $1 AND role = 'owner'`, orgA,
	).Scan(&owners); err != nil {
		t.Fatalf("count owners: %v", err)
	}
	if owners != 1 {
		t.Fatalf("expected 1 owner remaining, got %d", owners)
	}
}

func TestRemoveMember_RevokesRefreshTokens(t *testing.T) {
	h, repo := newTestHandler(t)
	orgA, _, _ := setupTwoOrgs(t, repo)
	adminID := seedMembership(t, repo, orgA)
	targetID := seedMembership(t, repo, orgA)
	ctx := context.Background()

	// seed a live refresh token for the target user in this org
	if err := repo.pool.QueryRow(ctx,
		`INSERT INTO refresh_tokens (user_id, org_id, family_id, token_hash, expires_at)
		 VALUES ($1, $2, gen_random_uuid(), $3, now() + interval '1 day')
		 RETURNING id`,
		targetID, orgA, "hash-"+uuid.NewString(),
	).Scan(new(uuid.UUID)); err != nil {
		t.Fatalf("seed token: %v", err)
	}

	r := chi.NewRouter()
	r.Delete("/members/{userId}", h.RemoveMember)

	req := httptest.NewRequest("DELETE", "/members/"+targetID.String(), nil)
	req = req.WithContext(inviteCtx(orgA, adminID, "admin"))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}

	var live int
	if err := repo.pool.QueryRow(ctx,
		`SELECT count(*) FROM refresh_tokens
		 WHERE user_id = $1 AND org_id = $2 AND revoked_at IS NULL`,
		targetID, orgA,
	).Scan(&live); err != nil {
		t.Fatalf("count tokens: %v", err)
	}
	if live != 0 {
		t.Fatalf("expected 0 live refresh tokens after removal, got %d", live)
	}
}
