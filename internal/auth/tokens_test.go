package auth

import (
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

func testManager() *TokenManager {
	return NewTokenManager("0123456789abcdef0123456789abcdef", 15*time.Minute, 30*24*time.Hour)
}

func TestIssueAccess_RoundTrip(t *testing.T) {
	m := testManager()
	uid := uuid.New()
	oid := uuid.New()

	tok, ttl, err := m.IssueAccess(uid, oid, "owner")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if ttl != 15*time.Minute {
		t.Fatalf("wrong ttl: %v", ttl)
	}

	claims, err := m.VerifyAccess(tok)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if claims.UserID != uid || claims.OrgID != oid || claims.Role != "owner" {
		t.Fatalf("claims mismatch: %+v", claims)
	}
}

func TestVerifyAccess_Expired(t *testing.T) {
	m := NewTokenManager("0123456789abcdef0123456789abcdef", -1*time.Minute, 30*24*time.Hour)
	tok, _, _ := m.IssueAccess(uuid.New(), uuid.New(), "owner")

	_, err := m.VerifyAccess(tok)
	if !errors.Is(err, ErrExpiredToken) {
		t.Fatalf("expected ErrExpiredToken, got %v", err)
	}
}

func TestVerifyAccess_Tampered(t *testing.T) {
	m := testManager()
	tok, _, _ := m.IssueAccess(uuid.New(), uuid.New(), "owner")

	tampered := tok[:len(tok)-4] + "xxxx"
	_, err := m.VerifyAccess(tampered)
	if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expected ErrInvalidToken, got %v", err)
	}
}

func TestVerifyAccess_AlgNone(t *testing.T) {
	m := testManager()

	claims := Claims{
		UserID: uuid.New(),
		OrgID:  uuid.New(),
		Role:   "owner",
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodNone, claims)
	noneSigned, err := tok.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("sign none: %v", err)
	}

	_, err = m.VerifyAccess(noneSigned)
	if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expected ErrInvalidToken for alg none, got %v", err)
	}
}

func TestVerifyAccess_WrongSecret(t *testing.T) {
	issuer := testManager()
	verifier := NewTokenManager("ffffffffffffffffffffffffffffffff", 15*time.Minute, 30*24*time.Hour)

	tok, _, _ := issuer.IssueAccess(uuid.New(), uuid.New(), "owner")
	_, err := verifier.VerifyAccess(tok)
	if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expected ErrInvalidToken, got %v", err)
	}
}

func TestNewRefreshToken_UniqueAndHashed(t *testing.T) {
	m := testManager()

	raw1, hash1, exp1, err := m.NewRefreshToken()
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	raw2, hash2, _, _ := m.NewRefreshToken()

	if raw1 == raw2 {
		t.Fatal("refresh tokens must be unique")
	}
	if hash1 == raw1 {
		t.Fatal("hash must not equal raw")
	}
	if hash1 != HashRefreshToken(raw1) {
		t.Fatal("hash mismatch")
	}
	if hash2 != HashRefreshToken(raw2) {
		t.Fatal("hash mismatch 2")
	}
	if time.Until(exp1) < 29*24*time.Hour {
		t.Fatalf("refresh ttl too short: %v", time.Until(exp1))
	}
}
