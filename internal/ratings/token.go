// Package ratings issues and verifies signed satisfaction-rating links and
// records one rating per ticket.
//
// A rating link is an opaque token signed with a dedicated HMAC secret. The
// token carries the ticket id, the customer id and an expiry, so the public
// endpoint can accept a rating without a session while still proving that the
// link came from a solved-ticket email. The ticket's org is resolved from the
// database, never trusted from the token.
package ratings

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Token errors.
var (
	ErrInvalidToken = errors.New("ratings: invalid token")
	ErrExpiredToken = errors.New("ratings: token expired")
)

// tokenVersion lets future token formats be rejected cleanly.
const tokenVersion = 1

// Claims is the verified content of a rating token.
type Claims struct {
	TicketID   uuid.UUID
	CustomerID uuid.UUID
	ExpiresAt  time.Time
}

// tokenPayload is the JSON body serialized before signing. Field names are
// short because the payload is embedded in an email link.
type tokenPayload struct {
	Version    int    `json:"v"`
	TicketID   string `json:"t"`
	CustomerID string `json:"c"`
	ExpiresAt  int64  `json:"e"`
}

// TokenManager signs and verifies rating tokens.
type TokenManager struct {
	secret []byte
	ttl    time.Duration
	now    func() time.Time
}

// NewTokenManager builds a manager with a dedicated rating secret. The secret
// must be non-empty; configuration enforces a minimum length.
func NewTokenManager(secret string, ttl time.Duration) *TokenManager {
	if ttl <= 0 {
		ttl = 30 * 24 * time.Hour
	}
	return &TokenManager{
		secret: []byte(secret),
		ttl:    ttl,
		now:    time.Now,
	}
}

// TTL returns the lifetime applied to new tokens.
func (m *TokenManager) TTL() time.Duration {
	return m.ttl
}

// Sign creates a token for one ticket/customer pair.
func (m *TokenManager) Sign(ticketID, customerID uuid.UUID) (string, error) {
	if len(m.secret) == 0 {
		return "", errors.New("ratings: token secret is not configured")
	}
	if ticketID == uuid.Nil || customerID == uuid.Nil {
		return "", errors.New("ratings: ticket and customer ids are required")
	}

	raw, err := json.Marshal(tokenPayload{
		Version:    tokenVersion,
		TicketID:   ticketID.String(),
		CustomerID: customerID.String(),
		ExpiresAt:  m.now().Add(m.ttl).Unix(),
	})
	if err != nil {
		return "", fmt.Errorf("ratings: marshal token: %w", err)
	}

	encoded := base64.RawURLEncoding.EncodeToString(raw)
	return encoded + "." + m.signature(encoded), nil
}

// Link returns the customer-facing URL carrying a fresh token.
func (m *TokenManager) Link(baseURL string, ticketID, customerID uuid.UUID) (string, error) {
	token, err := m.Sign(ticketID, customerID)
	if err != nil {
		return "", err
	}
	return BuildLink(baseURL, token), nil
}

// BuildLink appends a token to the customer-facing rating page. Separated from
// signing so callers and tests can use a recorded token.
func BuildLink(baseURL, token string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = "http://localhost:8080"
	}
	return baseURL + "/rate?token=" + url.QueryEscape(token)
}

// Verify checks the signature, format and expiry of token.
func (m *TokenManager) Verify(token string) (Claims, error) {
	if len(m.secret) == 0 {
		return Claims{}, ErrInvalidToken
	}

	encoded, signature, ok := strings.Cut(strings.TrimSpace(token), ".")
	if !ok || encoded == "" || signature == "" {
		return Claims{}, ErrInvalidToken
	}

	// Constant-time comparison: never use == for an HMAC.
	if !hmac.Equal([]byte(signature), []byte(m.signature(encoded))) {
		return Claims{}, ErrInvalidToken
	}

	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return Claims{}, ErrInvalidToken
	}
	var payload tokenPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return Claims{}, ErrInvalidToken
	}
	if payload.Version != tokenVersion {
		return Claims{}, ErrInvalidToken
	}

	ticketID, err := uuid.Parse(payload.TicketID)
	if err != nil || ticketID == uuid.Nil {
		return Claims{}, ErrInvalidToken
	}
	customerID, err := uuid.Parse(payload.CustomerID)
	if err != nil || customerID == uuid.Nil {
		return Claims{}, ErrInvalidToken
	}
	if payload.ExpiresAt <= 0 {
		return Claims{}, ErrInvalidToken
	}

	expiresAt := time.Unix(payload.ExpiresAt, 0).UTC()
	if !m.now().Before(expiresAt) {
		return Claims{}, ErrExpiredToken
	}

	return Claims{
		TicketID:   ticketID,
		CustomerID: customerID,
		ExpiresAt:  expiresAt,
	}, nil
}

func (m *TokenManager) signature(encoded string) string {
	mac := hmac.New(sha256.New, m.secret)
	mac.Write([]byte(encoded))
	return hex.EncodeToString(mac.Sum(nil))
}
