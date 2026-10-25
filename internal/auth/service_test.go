package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

type fakeRepo struct {
	createErr       error
	calls           int
	loginCandidate  *LoginCandidate
	createdRefresh  []RefreshToken
	rotateErr       error
	rotateUser      uuid.UUID
	rotateOrg       uuid.UUID
	rotateFamily    uuid.UUID
	findRefresh     *RefreshToken
	revokedFamilies []uuid.UUID
	revokedHashes   []string
	membershipRole  string
}

func (f *fakeRepo) CreateAccount(ctx context.Context, org Organization, user User) (Organization, User, Membership, error) {
	f.calls++
	if f.createErr != nil {
		return Organization{}, User{}, Membership{}, f.createErr
	}
	return org, user, Membership{Role: "owner"}, nil
}

func TestSignup_InvalidEmail(t *testing.T) {
	svc := NewService(&fakeRepo{}, nil)
	_, _, _, err := svc.Signup(context.Background(), SignupInput{
		OrgName: "Acme", Name: "Henri", Email: "not-an-email", Password: "password123",
	})
	if err != ErrInvalidEmail {
		t.Fatalf("expected ErrInvalidEmail, got %v", err)
	}
}

func TestSignup_WeakPassword(t *testing.T) {
	svc := NewService(&fakeRepo{}, nil)
	_, _, _, err := svc.Signup(context.Background(), SignupInput{
		OrgName: "Acme", Name: "Henri", Email: "h@acme.com", Password: "short",
	})
	if err != ErrWeakPassword {
		t.Fatalf("expected ErrWeakPassword, got %v", err)
	}
}

func TestSignup_EmailNormalized(t *testing.T) {
	svc := NewService(&fakeRepo{}, nil)
	_, user, _, err := svc.Signup(context.Background(), SignupInput{
		OrgName: "Acme", Name: "Henri", Email: "  H@ACME.COM  ", Password: "password123",
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if user.Email != "h@acme.com" {
		t.Fatalf("expected lowercase trimmed email, got %q", user.Email)
	}
}

func TestSignup_EmailTaken(t *testing.T) {
	svc := NewService(&fakeRepo{createErr: ErrEmailTaken}, nil)
	_, _, _, err := svc.Signup(context.Background(), SignupInput{
		OrgName: "Acme", Name: "Henri", Email: "h@acme.com", Password: "password123",
	})
	if err != ErrEmailTaken {
		t.Fatalf("expected ErrEmailTaken, got %v", err)
	}
}

func (f *fakeRepo) FindLoginCandidate(ctx context.Context, email string) (LoginCandidate, error) {
	if f.loginCandidate == nil {
		return LoginCandidate{}, ErrInvalidCredentials
	}
	return *f.loginCandidate, nil
}

func (f *fakeRepo) CreateRefreshToken(ctx context.Context, t RefreshToken) error {
	f.createdRefresh = append(f.createdRefresh, t)
	return nil
}

func (f *fakeRepo) RotateRefreshToken(ctx context.Context, hash string) (uuid.UUID, uuid.UUID, uuid.UUID, error) {
	if f.rotateErr != nil {
		return uuid.Nil, uuid.Nil, uuid.Nil, f.rotateErr
	}
	return f.rotateUser, f.rotateOrg, f.rotateFamily, nil
}

func (f *fakeRepo) FindRefreshTokenByHash(ctx context.Context, hash string) (RefreshToken, error) {
	if f.findRefresh == nil {
		return RefreshToken{}, ErrInvalidToken
	}
	return *f.findRefresh, nil
}

func (f *fakeRepo) RevokeFamily(ctx context.Context, familyID uuid.UUID) error {
	f.revokedFamilies = append(f.revokedFamilies, familyID)
	return nil
}

func (f *fakeRepo) RevokeByHash(ctx context.Context, hash string) error {
	f.revokedHashes = append(f.revokedHashes, hash)
	return nil
}

func (f *fakeRepo) FindMembership(ctx context.Context, userID, orgID uuid.UUID) (string, error) {
	if f.membershipRole == "" {
		return "", ErrInvalidToken
	}
	return f.membershipRole, nil
}

func testTokenManager() *TokenManager {
	return NewTokenManager("0123456789abcdef0123456789abcdef", 15*time.Minute, 30*24*time.Hour)
}

func TestLogin_UnknownEmailAndWrongPassword_Identical(t *testing.T) {
	svc := NewService(&fakeRepo{}, nil)
	_, err1 := svc.Login(context.Background(), LoginInput{Email: "no@x.com", Password: "pw"})

	hash, _ := bcrypt.GenerateFromPassword([]byte("correct"), bcrypt.DefaultCost)
	svc2 := NewService(&fakeRepo{
		loginCandidate: &LoginCandidate{
			UserID:       uuid.New(),
			Email:        "yes@x.com",
			PasswordHash: string(hash),
			Memberships:  []MembershipInfo{{OrgID: uuid.New(), OrgSlug: "acme", Role: "owner"}},
		},
	}, testTokenManager())
	_, err2 := svc2.Login(context.Background(), LoginInput{Email: "yes@x.com", Password: "wrong"})

	if !errors.Is(err1, ErrInvalidCredentials) {
		t.Fatalf("unknown email: expected ErrInvalidCredentials, got %v", err1)
	}
	if !errors.Is(err2, ErrInvalidCredentials) {
		t.Fatalf("wrong password: expected ErrInvalidCredentials, got %v", err2)
	}
}

func TestLogin_SingleOrgAutoSelects(t *testing.T) {
	hash, _ := bcrypt.GenerateFromPassword([]byte("correct"), bcrypt.DefaultCost)
	orgID := uuid.New()
	f := &fakeRepo{
		loginCandidate: &LoginCandidate{
			UserID:       uuid.New(),
			Email:        "h@acme.com",
			PasswordHash: string(hash),
			Memberships:  []MembershipInfo{{OrgID: orgID, OrgSlug: "acme", Role: "owner"}},
		},
	}
	svc := NewService(f, testTokenManager())

	pair, err := svc.Login(context.Background(), LoginInput{Email: "h@acme.com", Password: "correct"})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if pair.Principal.OrgID != orgID {
		t.Fatalf("wrong org: %v", pair.Principal.OrgID)
	}
	if pair.AccessToken == "" || pair.RefreshToken == "" {
		t.Fatal("empty tokens")
	}
}

func TestLogin_MultipleOrgsWithoutSlug_ReturnsOrgRequired(t *testing.T) {
	hash, _ := bcrypt.GenerateFromPassword([]byte("correct"), bcrypt.DefaultCost)
	f := &fakeRepo{
		loginCandidate: &LoginCandidate{
			UserID:       uuid.New(),
			Email:        "h@acme.com",
			PasswordHash: string(hash),
			Memberships: []MembershipInfo{
				{OrgID: uuid.New(), OrgSlug: "acme", Role: "owner"},
				{OrgID: uuid.New(), OrgSlug: "other", Role: "agent"},
			},
		},
	}
	svc := NewService(f, testTokenManager())

	_, err := svc.Login(context.Background(), LoginInput{Email: "h@acme.com", Password: "correct"})
	if !errors.Is(err, ErrOrgRequired) {
		t.Fatalf("expected ErrOrgRequired, got %v", err)
	}
}

func TestLogin_MultipleOrgsWithSlug_SelectsMatch(t *testing.T) {
	hash, _ := bcrypt.GenerateFromPassword([]byte("correct"), bcrypt.DefaultCost)
	wantOrg := uuid.New()
	f := &fakeRepo{
		loginCandidate: &LoginCandidate{
			UserID:       uuid.New(),
			Email:        "h@acme.com",
			PasswordHash: string(hash),
			Memberships: []MembershipInfo{
				{OrgID: uuid.New(), OrgSlug: "acme", Role: "owner"},
				{OrgID: wantOrg, OrgSlug: "other", Role: "agent"},
			},
		},
	}
	svc := NewService(f, testTokenManager())

	pair, err := svc.Login(context.Background(), LoginInput{Email: "h@acme.com", Password: "correct", OrgSlug: "other"})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if pair.Principal.OrgID != wantOrg {
		t.Fatalf("wrong org: %v", pair.Principal.OrgID)
	}
	if pair.Principal.Role != "agent" {
		t.Fatalf("wrong role: %v", pair.Principal.Role)
	}
}

func TestRefresh_ReuseTriggersFamilyRevoke(t *testing.T) {
	family := uuid.New()
	now := time.Now()
	f := &fakeRepo{
		rotateErr:   ErrInvalidToken,
		findRefresh: &RefreshToken{FamilyID: family, RevokedAt: &now},
	}
	svc := NewService(f, testTokenManager())

	_, err := svc.Refresh(context.Background(), "raw", "ua", "ip")
	if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expected ErrInvalidToken, got %v", err)
	}
	if len(f.revokedFamilies) != 1 || f.revokedFamilies[0] != family {
		t.Fatalf("expected family %v revoked, got %v", family, f.revokedFamilies)
	}
}

func TestRefresh_UnknownToken_NoRevoke(t *testing.T) {
	f := &fakeRepo{rotateErr: ErrInvalidToken}
	svc := NewService(f, testTokenManager())

	_, err := svc.Refresh(context.Background(), "raw", "ua", "ip")
	if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expected ErrInvalidToken, got %v", err)
	}
	if len(f.revokedFamilies) != 0 {
		t.Fatalf("unknown token should not revoke, got %v", f.revokedFamilies)
	}
}

func TestLogout_RevokesByHash(t *testing.T) {
	f := &fakeRepo{}
	svc := NewService(f, testTokenManager())

	if err := svc.Logout(context.Background(), "raw"); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if len(f.revokedHashes) != 1 {
		t.Fatalf("expected 1 revoked hash, got %d", len(f.revokedHashes))
	}
	if f.revokedHashes[0] != HashRefreshToken("raw") {
		t.Fatalf("wrong hash revoked: %v", f.revokedHashes[0])
	}
}
