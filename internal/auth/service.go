package auth

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"golang.org/x/crypto/bcrypt"
	"crypto/rand"
)

type Repo interface {
	CreateAccount(ctx context.Context, org Organization, user User) (Organization, User, Membership, error)
}

type Service struct {
	repo Repo
}

func NewService(repo Repo) *Service {
	return &Service{repo: repo}
}

type SignupInput struct {
	OrgName  string
	Name     string
	Email    string
	Password string
}

var emailRe = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)

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
