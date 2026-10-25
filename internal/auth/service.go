package auth

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
	"crypto/rand"
	"github.com/HenriNorman/gregport/internal/platform/requestctx"
	"github.com/google/uuid"
)

type Repo interface {
	CreateAccount(ctx context.Context, org Organization, user User) (Organization, User, Membership, error)
	FindLoginCandidate(ctx context.Context, email string) (LoginCandidate, error)
	CreateRefreshToken(ctx context.Context, t RefreshToken) error
	RotateRefreshToken(ctx context.Context, hash string) (userID, orgID, familyID uuid.UUID, err error)
	FindRefreshTokenByHash(ctx context.Context, hash string) (RefreshToken, error)
	RevokeFamily(ctx context.Context, familyID uuid.UUID) error
	RevokeByHash(ctx context.Context, hash string) error
	FindMembership(ctx context.Context, userID, orgID uuid.UUID) (string, error)
}

type Service struct {
	repo Repo
	tokens *TokenManager
}

func NewService(repo Repo, tokens *TokenManager) *Service {
	return &Service{repo: repo, tokens: tokens}
}

type SignupInput struct {
	OrgName  string
	Name     string
	Email    string
	Password string
}

type LoginInput struct {
	Email    string
	Password string
	OrgSlug  string
	UserAgent string
	IP        string
}

type TokenPair struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    time.Duration
	Principal    requestctx.Principal
}

var emailRe = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)

var dummyHash = []byte("$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy")

func (s *Service) Signup(ctx context.Context, in SignupInput) (Organization, User, Membership, error) {
	// 1. normalize
	in.Email = strings.ToLower(strings.TrimSpace(in.Email))
	in.Name = strings.TrimSpace(in.Name)
	in.OrgName = strings.TrimSpace(in.OrgName)

	// 2. validate
	if !emailRe.MatchString(in.Email) {
		return Organization{}, User{}, Membership{}, ErrInvalidEmail
	}
	if in.Name == "" {
		return Organization{}, User{}, Membership{}, ErrEmptyName
	}
	if in.OrgName == "" {
		return Organization{}, User{}, Membership{}, ErrInvalidInput
	}
	if len(in.Password) < 8 {
		return Organization{}, User{}, Membership{}, ErrWeakPassword
	}
	if len(in.Password) > 72 {
		return Organization{}, User{}, Membership{}, ErrWeakPassword
	}

	// 3. hash
	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
	if err != nil {
		return Organization{}, User{}, Membership{}, fmt.Errorf("hash: %w", err)
	}

	// 4. slug with retry on collision
	org := Organization{Name: in.OrgName, Slug: slugify(in.OrgName)}
	user := User{Email: in.Email, Name: in.Name, PasswordHash: string(hash)}

	for attempt := 0; attempt < 3; attempt++ {
		outOrg, outUser, outMember, err := s.repo.CreateAccount(ctx, org, user)
		if err == nil {
			return outOrg, outUser, outMember, nil
		}
		if errors.Is(err, ErrSlugTaken) && attempt < 2 {
			org.Slug = slugify(in.OrgName) + "-" + randomSuffix()
			continue
		}
		return Organization{}, User{}, Membership{}, err
	}

	return Organization{}, User{}, Membership{}, ErrSlugTaken
}

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_':
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "org"
	}
	return out
}

func randomSuffix() string {
	const letters = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "000000"
	}
	for i := range b {
		b[i] = letters[int(b[i])%len(letters)]
	}
	return string(b)
}

func (s *Service) Login(ctx context.Context, in LoginInput) (TokenPair, error) {
	email := strings.ToLower(strings.TrimSpace(in.Email))

	cand, err := s.repo.FindLoginCandidate(ctx, email)
	if err != nil {
		if errors.Is(err, ErrInvalidCredentials) {
			// constant-time defense: do a bcrypt comparison anyway
			_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(in.Password))
			return TokenPair{}, ErrInvalidCredentials
		}
		return TokenPair{}, err
	}

	if err := bcrypt.CompareHashAndPassword([]byte(cand.PasswordHash), []byte(in.Password)); err != nil {
		return TokenPair{}, ErrInvalidCredentials
	}

	mem, err := pickMembership(cand.Memberships, in.OrgSlug)
	if err != nil {
		return TokenPair{}, err
	}

	access, ttl, err := s.tokens.IssueAccess(cand.UserID, mem.OrgID, mem.Role)
	if err != nil {
		return TokenPair{}, fmt.Errorf("issue access: %w", err)
	}

	raw, hash, expiresAt, err := s.tokens.NewRefreshToken()
	if err != nil {
		return TokenPair{}, fmt.Errorf("new refresh: %w", err)
	}

	if err := s.repo.CreateRefreshToken(ctx, RefreshToken{
		UserID:    cand.UserID,
		OrgID:     mem.OrgID,
		FamilyID:  uuid.New(),
		TokenHash: hash,
		UserAgent: in.UserAgent,
		IP:        in.IP,
		ExpiresAt: expiresAt,
	}); err != nil {
		return TokenPair{}, err
	}

	return TokenPair{
		AccessToken:  access,
		RefreshToken: raw,
		ExpiresIn:    ttl,
		Principal: requestctx.Principal{
			UserID: cand.UserID,
			OrgID:  mem.OrgID,
			Role:   mem.Role,
		},
	}, nil
}

func pickMembership(mems []MembershipInfo, orgSlug string) (MembershipInfo, error) {
	if orgSlug != "" {
		for _, m := range mems {
			if m.OrgSlug == orgSlug {
				return m, nil
			}
		}
		// Don't reveal whether the org exists — same error as bad credentials.
		return MembershipInfo{}, ErrInvalidCredentials
	}
	switch len(mems) {
	case 0:
		return MembershipInfo{}, ErrInvalidCredentials
	case 1:
		return mems[0], nil
	default:
		return MembershipInfo{}, ErrOrgRequired
	}
}

func (s *Service) Refresh(ctx context.Context, rawRefresh string, userAgent, ip string) (TokenPair, error) {
	hash := HashRefreshToken(rawRefresh)

	userID, orgID, familyID, err := s.repo.RotateRefreshToken(ctx, hash)
	if err != nil {
		if !errors.Is(err, ErrInvalidToken) {
			return TokenPair{}, err
		}

		// distinguish "unknown" from "reuse"
		old, findErr := s.repo.FindRefreshTokenByHash(ctx, hash)
		if findErr != nil {
			return TokenPair{}, ErrInvalidToken
		}
		if old.RevokedAt != nil {
			// reuse detected — revoke the whole family
			_ = s.repo.RevokeFamily(ctx, old.FamilyID)
		}
		return TokenPair{}, ErrInvalidToken
	}

	role, err := s.repo.FindMembership(ctx, userID, orgID)
	if err != nil {
		_ = s.repo.RevokeFamily(ctx, familyID)
		return TokenPair{}, ErrInvalidToken
	}

	access, ttl, err := s.tokens.IssueAccess(userID, orgID, role)
	if err != nil {
		return TokenPair{}, fmt.Errorf("issue access: %w", err)
	}

	raw, newHash, expiresAt, err := s.tokens.NewRefreshToken()
	if err != nil {
		return TokenPair{}, fmt.Errorf("new refresh: %w", err)
	}

	if err := s.repo.CreateRefreshToken(ctx, RefreshToken{
		UserID:    userID,
		OrgID:     orgID,
		FamilyID:  familyID,
		TokenHash: newHash,
		UserAgent: userAgent,
		IP:        ip,
		ExpiresAt: expiresAt,
	}); err != nil {
		return TokenPair{}, err
	}

	return TokenPair{
		AccessToken:  access,
		RefreshToken: raw,
		ExpiresIn:    ttl,
		Principal: requestctx.Principal{
			UserID: userID,
			OrgID:  orgID,
			Role:   role,
		},
	}, nil
}

func (s *Service) Logout(ctx context.Context, rawRefresh string) error {
	if rawRefresh == "" {
		return nil
	}
	return s.repo.RevokeByHash(ctx, HashRefreshToken(rawRefresh))
}
