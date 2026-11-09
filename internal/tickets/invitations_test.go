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
	"golang.org/x/crypto/bcrypt"

	"github.com/HenriNorman/gregport/internal/platform/requestctx"
)

// inviteCtx builds a principal for the given role with Kind = user.
func inviteCtx(orgID, actorID uuid.UUID, role string) context.Context {
	return requestctx.WithPrincipal(context.Background(), requestctx.Principal{
		Kind:    requestctx.KindUser,
		ActorID: actorID,
		OrgID:   orgID,
		Role:    role,
	})
}

func TestCreateInvitation_OwnerInvitesAgent(t *testing.T) {
	h, repo := newTestHandler(t)
	orgA, _, _ := setupTwoOrgs(t, repo)
	ownerID := seedMembership(t, repo, orgA)

	r := chi.NewRouter()
	r.Post("/invitations", h.CreateInvitation)

	body, _ := json.Marshal(CreateInvitationRequest{Email: "newagent@test.local", Role: "agent"})
	req := httptest.NewRequest("POST", "/invitations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(inviteCtx(orgA, ownerID, "owner"))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp CreateInvitationResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Token == "" {
		t.Fatal("expected raw token in create response")
	}
	if resp.Invitation.Email != "newagent@test.local" || resp.Invitation.Role != "agent" {
		t.Fatalf("invitation mismatch: %+v", resp.Invitation)
	}

	// token is hashed in DB, not stored raw
	var rawCount int
	_ = repo.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM invitations WHERE token_hash = $1`, resp.Token).Scan(&rawCount)
	if rawCount != 0 {
		t.Fatal("raw token must not be stored")
	}

	// audit row written
	var auditCount int
	_ = repo.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_logs
		 WHERE org_id = $1 AND action = 'invitation.created'`, orgA).Scan(&auditCount)
	if auditCount != 1 {
		t.Fatalf("expected 1 invitation.created audit row, got %d", auditCount)
	}
}

func TestCreateInvitation_AdminInvitesAdmin_403(t *testing.T) {
	h, repo := newTestHandler(t)
	orgA, _, _ := setupTwoOrgs(t, repo)
	adminID := seedMembership(t, repo, orgA)

	r := chi.NewRouter()
	r.Post("/invitations", h.CreateInvitation)

	body, _ := json.Marshal(CreateInvitationRequest{Email: "x@test.local", Role: "admin"})
	req := httptest.NewRequest("POST", "/invitations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(inviteCtx(orgA, adminID, "admin"))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCreateInvitation_OwnerInvitesOwner_403(t *testing.T) {
	h, repo := newTestHandler(t)
	orgA, _, _ := setupTwoOrgs(t, repo)
	ownerID := seedMembership(t, repo, orgA)

	r := chi.NewRouter()
	r.Post("/invitations", h.CreateInvitation)

	body, _ := json.Marshal(CreateInvitationRequest{Email: "x@test.local", Role: "owner"})
	req := httptest.NewRequest("POST", "/invitations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(inviteCtx(orgA, ownerID, "owner"))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAcceptInvitation_NewUser(t *testing.T) {
	h, repo := newTestHandler(t)
	orgA, _, _ := setupTwoOrgs(t, repo)
	ownerID := seedMembership(t, repo, orgA)
	ctx := context.Background()
	email := "newuser-" + uuid.NewString()[:8] + "@test.local"

	// seed an invitation directly
	raw := "raw-invite-token-" + uuid.NewString()
	hash := HashInvitationToken(raw)
	if _, err := repo.CreateInvitation(ctx, Invitation{
		OrgID:     orgA,
		Email:     email,
		Role:      "agent",
		TokenHash: hash,
		InvitedBy: ownerID,
		ExpiresAt: time.Now().Add(7 * 24 * time.Hour),
	}); err != nil {
		t.Fatalf("seed invitation: %v", err)
	}

	r := chi.NewRouter()
	r.Post("/invitations/accept", h.AcceptInvitation)

	body, _ := json.Marshal(AcceptInvitationRequest{
		Token:    raw,
		Name:     "New User",
		Password: "password123",
	})
	req := httptest.NewRequest("POST", "/invitations/accept", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp AcceptInvitationResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.User.Email != email {
		t.Fatalf("email mismatch: expected %s, got %s", email, resp.User.Email)
	}
	if resp.Role != "agent" {
		t.Fatalf("role mismatch: expected agent, got %s", resp.Role)
	}

	// user and membership exist
	var userCount, memCount int
	_ = repo.pool.QueryRow(ctx,
		`SELECT count(*) FROM users WHERE email = $1`, email).Scan(&userCount)
	if userCount != 1 {
		t.Fatalf("expected 1 user, got %d", userCount)
	}
	_ = repo.pool.QueryRow(ctx,
		`SELECT count(*) FROM memberships m JOIN users u ON u.id = m.user_id
		 WHERE u.email = $1 AND m.org_id = $2`, email, orgA).Scan(&memCount)
	if memCount != 1 {
		t.Fatalf("expected 1 membership, got %d", memCount)
	}

	// audit row written
	var auditCount int
	_ = repo.pool.QueryRow(ctx,
		`SELECT count(*) FROM audit_logs WHERE org_id = $1 AND action = 'invitation.accepted'`,
		orgA).Scan(&auditCount)
	if auditCount != 1 {
		t.Fatalf("expected 1 invitation.accepted audit row, got %d", auditCount)
	}
}

func TestAcceptInvitation_ExistingUser_MismatchEmail_403(t *testing.T) {
	h, repo := newTestHandler(t)
	orgA, _, _ := setupTwoOrgs(t, repo)
	ownerID := seedMembership(t, repo, orgA)
	otherID := seedMembership(t, repo, orgA)
	ctx := context.Background()

	// invitation is for someone else's email
	raw := "raw-invite-" + uuid.NewString()
	if _, err := repo.CreateInvitation(ctx, Invitation{
		OrgID:     orgA,
		Email:     "someone-else@test.local",
		Role:      "agent",
		TokenHash: HashInvitationToken(raw),
		InvitedBy: ownerID,
		ExpiresAt: time.Now().Add(7 * 24 * time.Hour),
	}); err != nil {
		t.Fatalf("seed invitation: %v", err)
	}

	r := chi.NewRouter()
	r.Post("/invitations/accept", h.AcceptInvitation)

	body, _ := json.Marshal(AcceptInvitationRequest{Token: raw})
	req := httptest.NewRequest("POST", "/invitations/accept", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(inviteCtx(orgA, otherID, "agent"))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for email mismatch, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAcceptInvitation_Expired_422(t *testing.T) {
	h, repo := newTestHandler(t)
	orgA, _, _ := setupTwoOrgs(t, repo)
	ownerID := seedMembership(t, repo, orgA)
	ctx := context.Background()

	raw := "raw-invite-" + uuid.NewString()
	if _, err := repo.CreateInvitation(ctx, Invitation{
		OrgID:     orgA,
		Email:     "expired@test.local",
		Role:      "agent",
		TokenHash: HashInvitationToken(raw),
		InvitedBy: ownerID,
		ExpiresAt: time.Now().Add(-time.Hour), // expired
	}); err != nil {
		t.Fatalf("seed invitation: %v", err)
	}

	r := chi.NewRouter()
	r.Post("/invitations/accept", h.AcceptInvitation)

	body, _ := json.Marshal(AcceptInvitationRequest{
		Token:    raw,
		Name:     "X",
		Password: "password123",
	})
	req := httptest.NewRequest("POST", "/invitations/accept", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for expired, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAcceptInvitation_Used_422(t *testing.T) {
	h, repo := newTestHandler(t)
	orgA, _, _ := setupTwoOrgs(t, repo)
	ownerID := seedMembership(t, repo, orgA)
	ctx := context.Background()

	raw := "raw-invite-" + uuid.NewString()
	inv, err := repo.CreateInvitation(ctx, Invitation{
		OrgID:     orgA,
		Email:     "used@test.local",
		Role:      "agent",
		TokenHash: HashInvitationToken(raw),
		InvitedBy: ownerID,
		ExpiresAt: time.Now().Add(7 * 24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("seed invitation: %v", err)
	}
	// mark it accepted
	if _, err := repo.pool.Exec(ctx,
		`UPDATE invitations SET accepted_at = now() WHERE id = $1`, inv.ID,
	); err != nil {
		t.Fatalf("mark accepted: %v", err)
	}

	r := chi.NewRouter()
	r.Post("/invitations/accept", h.AcceptInvitation)

	body, _ := json.Marshal(AcceptInvitationRequest{
		Token:    raw,
		Name:     "X",
		Password: "password123",
	})
	req := httptest.NewRequest("POST", "/invitations/accept", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for used, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAcceptInvitation_DuplicateMembership_409(t *testing.T) {
	h, repo := newTestHandler(t)
	orgA, _, _ := setupTwoOrgs(t, repo)
	existingID := seedMembership(t, repo, orgA)
	ctx := context.Background()

	var existingEmail string
	if err := repo.pool.QueryRow(ctx,
		`SELECT email FROM users WHERE id = $1`, existingID,
	).Scan(&existingEmail); err != nil {
		t.Fatalf("email: %v", err)
	}

	raw := "raw-invite-" + uuid.NewString()
	if _, err := repo.CreateInvitation(ctx, Invitation{
		OrgID:     orgA,
		Email:     existingEmail,
		Role:      "agent",
		TokenHash: HashInvitationToken(raw),
		InvitedBy: existingID,
		ExpiresAt: time.Now().Add(7 * 24 * time.Hour),
	}); err != nil {
		t.Fatalf("seed invitation: %v", err)
	}

	r := chi.NewRouter()
	r.Post("/invitations/accept", h.AcceptInvitation)

	body, _ := json.Marshal(AcceptInvitationRequest{Token: raw})
	req := httptest.NewRequest("POST", "/invitations/accept", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(inviteCtx(orgA, existingID, "agent"))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 for duplicate membership, got %d: %s", rec.Code, rec.Body.String())
	}
}

// silence unused import if bcrypt ends up unused in this file
var _ = bcrypt.DefaultCost
