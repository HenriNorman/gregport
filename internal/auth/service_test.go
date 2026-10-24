package auth

import (
	"context"
	"testing"
)

type fakeRepo struct {
	createErr error
	calls     int
}

func (f *fakeRepo) CreateAccount(ctx context.Context, org Organization, user User) (Organization, User, Membership, error) {
	f.calls++
	if f.createErr != nil {
		return Organization{}, User{}, Membership{}, f.createErr
	}
	return org, user, Membership{Role: "owner"}, nil
}

func TestSignup_InvalidEmail(t *testing.T) {
	svc := NewService(&fakeRepo{})
	_, _, _, err := svc.Signup(context.Background(), SignupInput{
		OrgName: "Acme", Name: "Henri", Email: "not-an-email", Password: "password123",
	})
	if err != ErrInvalidEmail {
		t.Fatalf("expected ErrInvalidEmail, got %v", err)
	}
}

func TestSignup_WeakPassword(t *testing.T) {
	svc := NewService(&fakeRepo{})
	_, _, _, err := svc.Signup(context.Background(), SignupInput{
		OrgName: "Acme", Name: "Henri", Email: "h@acme.com", Password: "short",
	})
	if err != ErrWeakPassword {
		t.Fatalf("expected ErrWeakPassword, got %v", err)
	}
}

func TestSignup_EmailNormalized(t *testing.T) {
	svc := NewService(&fakeRepo{})
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
	svc := NewService(&fakeRepo{createErr: ErrEmailTaken})
	_, _, _, err := svc.Signup(context.Background(), SignupInput{
		OrgName: "Acme", Name: "Henri", Email: "h@acme.com", Password: "password123",
	})
	if err != ErrEmailTaken {
		t.Fatalf("expected ErrEmailTaken, got %v", err)
	}
}
