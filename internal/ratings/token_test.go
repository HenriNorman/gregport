package ratings

import (
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func testTokens() *TokenManager {
	return NewTokenManager(strings.Repeat("s", 32), 24*time.Hour)
}

func TestTokenSignAndVerify(t *testing.T) {
	m := testTokens()
	ticketID, customerID := uuid.New(), uuid.New()

	token, err := m.Sign(ticketID, customerID)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if strings.Count(token, ".") != 1 {
		t.Fatalf("token format: %q", token)
	}

	claims, err := m.Verify(token)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if claims.TicketID != ticketID || claims.CustomerID != customerID {
		t.Fatalf("claims: %+v", claims)
	}
	if time.Until(claims.ExpiresAt) < 23*time.Hour {
		t.Fatalf("expiry: %s", claims.ExpiresAt)
	}
}

func TestTokenVerifyRejectsTampering(t *testing.T) {
	m := testTokens()
	token, err := m.Sign(uuid.New(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}

	encoded, signature, _ := strings.Cut(token, ".")

	tests := []struct {
		name  string
		token string
	}{
		{name: "empty", token: ""},
		{name: "missing signature", token: encoded},
		{name: "missing payload", token: "." + signature},
		{name: "flipped signature", token: encoded + "." + strings.Repeat("0", len(signature))},
		{name: "flipped payload", token: "x" + encoded + "." + signature},
		{name: "appended payload", token: encoded + "x." + signature},
		{name: "extra segment", token: token + ".x"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := m.Verify(tt.token); !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("err=%v, want ErrInvalidToken", err)
			}
		})
	}
}

func TestTokenVerifyRejectsExpired(t *testing.T) {
	m := testTokens()
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }

	token, err := m.Sign(uuid.New(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Verify(token); err != nil {
		t.Fatalf("fresh token: %v", err)
	}

	m.now = func() time.Time { return now.Add(m.TTL()) }
	if _, err := m.Verify(token); !errors.Is(err, ErrExpiredToken) {
		t.Fatalf("err=%v, want ErrExpiredToken", err)
	}
}

func TestTokenVerifyRejectsOtherSecret(t *testing.T) {
	ticketID, customerID := uuid.New(), uuid.New()
	token, err := NewTokenManager(strings.Repeat("a", 32), time.Hour).Sign(ticketID, customerID)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := NewTokenManager(strings.Repeat("b", 32), time.Hour).Verify(token); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("err=%v, want ErrInvalidToken", err)
	}
}

func TestTokenSignValidation(t *testing.T) {
	m := testTokens()
	if _, err := m.Sign(uuid.Nil, uuid.New()); err == nil {
		t.Fatal("expected an error for a missing ticket id")
	}
	if _, err := m.Sign(uuid.New(), uuid.Nil); err == nil {
		t.Fatal("expected an error for a missing customer id")
	}
	if _, err := NewTokenManager("", time.Hour).Sign(uuid.New(), uuid.New()); err == nil {
		t.Fatal("expected an error for an empty secret")
	}
}

func TestTokenDefaultTTL(t *testing.T) {
	m := NewTokenManager("secret", 0)
	if m.TTL() != 30*24*time.Hour {
		t.Fatalf("ttl=%s", m.TTL())
	}
}

func TestBuildLink(t *testing.T) {
	link := BuildLink("https://support.example.com/", "abc.def")
	parsed, err := url.Parse(link)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.Scheme != "https" || parsed.Host != "support.example.com" || parsed.Path != "/rate" {
		t.Fatalf("link=%s", link)
	}
	if parsed.Query().Get("token") != "abc.def" {
		t.Fatalf("token=%q", parsed.Query().Get("token"))
	}

	fallback := BuildLink("", "t")
	if !strings.HasPrefix(fallback, "http://localhost:8080/rate?token=") {
		t.Fatalf("fallback=%s", fallback)
	}
}

func TestTokenManagerLink(t *testing.T) {
	m := testTokens()
	ticketID, customerID := uuid.New(), uuid.New()

	link, err := m.Link("https://support.example.com", ticketID, customerID)
	if err != nil {
		t.Fatalf("link: %v", err)
	}
	parsed, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := m.Verify(parsed.Query().Get("token"))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if claims.TicketID != ticketID || claims.CustomerID != customerID {
		t.Fatalf("claims: %+v", claims)
	}
}
