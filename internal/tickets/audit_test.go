package tickets

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestUpdateMemberRole_AuditRow(t *testing.T) {
	h, repo := newTestHandler(t)
	orgA, _, _ := setupTwoOrgs(t, repo)
	ownerID := seedMembership(t, repo, orgA)
	targetID := seedMembership(t, repo, orgA)
	ctx := context.Background()

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

	var count int
	_ = repo.pool.QueryRow(ctx,
		`SELECT count(*) FROM audit_logs
		 WHERE org_id = $1 AND action = 'member.role_changed' AND entity_id = $2`,
		orgA, targetID,
	).Scan(&count)
	if count != 1 {
		t.Fatalf("expected 1 member.role_changed audit row, got %d", count)
	}
}

func TestRemoveMember_AuditRow(t *testing.T) {
	h, repo := newTestHandler(t)
	orgA, _, _ := setupTwoOrgs(t, repo)
	adminID := seedMembership(t, repo, orgA)
	targetID := seedMembership(t, repo, orgA)
	ctx := context.Background()

	r := chi.NewRouter()
	r.Delete("/members/{userId}", h.RemoveMember)

	req := httptest.NewRequest("DELETE", "/members/"+targetID.String(), nil)
	req = req.WithContext(inviteCtx(orgA, adminID, "admin"))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}

	var count int
	_ = repo.pool.QueryRow(ctx,
		`SELECT count(*) FROM audit_logs
		 WHERE org_id = $1 AND action = 'member.removed' AND entity_id = $2`,
		orgA, targetID,
	).Scan(&count)
	if count != 1 {
		t.Fatalf("expected 1 member.removed audit row, got %d", count)
	}
}
