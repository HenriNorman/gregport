// Package inboundemail receives inbound email from a provider, verifies the
// provider signature, deduplicates on Message-ID, and routes each message:
// a subject containing `[#1042]` or an `In-Reply-To` referencing a previously
// received message appends a customer comment to an existing ticket,
// otherwise a new ticket (channel "email") is created from the message.
//
// Every received message is recorded in inbound_emails so the raw email can
// be replayed or audited later.
package inboundemail

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// DefaultSignatureTolerance is how far a provider-signed timestamp may drift
// from the current time before the message is rejected.
const DefaultSignatureTolerance = 5 * time.Minute

// Header names the email provider is expected to send.
const (
	HeaderTimestamp = "X-Inbound-Email-Timestamp"
	HeaderSignature = "X-Inbound-Email-Signature"
)

// Signature scheme: hex(HMAC-SHA256(secret, timestamp + "." + body)).

// ErrInvalidSignature is returned when the provider signature is missing or
// does not match. It is a permanent rejection: retrying cannot succeed.
var ErrInvalidSignature = errors.New("inboundemail: invalid provider signature")

// ErrExpiredSignature is returned when the signed timestamp is outside the
// tolerance window.
var ErrExpiredSignature = errors.New("inboundemail: signature timestamp is too old")

// ErrDuplicateMessageID is returned when a Message-ID was already processed.
var ErrDuplicateMessageID = errors.New("inboundemail: duplicate message id")

// ErrOrgNotFound is returned when no org owns the recipient address.
var ErrOrgNotFound = errors.New("inboundemail: no org owns the recipient address")

// ErrTicketNotFound is returned when the referenced ticket does not exist.
var ErrTicketNotFound = errors.New("inboundemail: ticket not found")

// ErrInvalidInput is returned for malformed or incomplete messages.
var ErrInvalidInput = errors.New("inboundemail: invalid input")

// InboundEmail is a parsed incoming email message.
type InboundEmail struct {
	MessageID string
	InReplyTo string
	FromEmail string
	FromName  string
	Recipient string
	Subject   string
	Body      string
	Date      time.Time
	Raw       []byte
}

// Ticket is a minimal ticket reference returned by ticket creation.
type Ticket struct {
	ID     uuid.UUID
	Number int64
}

// Comment is a minimal comment reference returned by comment creation.
type Comment struct {
	ID uuid.UUID
}

// InboundEmailRow is one stored inbound_emails row.
type InboundEmailRow struct {
	ID         uuid.UUID
	OrgID      uuid.UUID
	MessageID  string
	InReplyTo  string
	FromEmail  string
	FromName   string
	Recipient  string
	Subject    string
	Body       string
	Status     string
	TicketID   *uuid.UUID
	CommentID  *uuid.UUID
	ReceivedAt time.Time
}

// VerifySignature reports whether signature is the HMAC-SHA256 of
// timestamp + "." + body under secret, compared in constant time.
func VerifySignature(secret, timestamp string, body, signature []byte) bool {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(body)
	expected := mac.Sum(nil)
	sig, err := hex.DecodeString(strings.TrimSpace(string(signature)))
	if err != nil {
		return false
	}
	if len(sig) != len(expected) {
		return false
	}
	return hmac.Equal(sig, expected)
}

// VerifyTimestamp reports whether timestamp is within tolerance of now.
func VerifyTimestamp(timestamp string, now time.Time, tolerance time.Duration) error {
	sec, err := parseUnixSeconds(timestamp)
	if err != nil {
		return ErrExpiredSignature
	}
	signedAt := time.Unix(sec, 0).UTC()
	if now.Sub(signedAt) > tolerance || signedAt.Sub(now) > tolerance {
		return ErrExpiredSignature
	}
	return nil
}

func parseUnixSeconds(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("empty timestamp")
	}
	var neg bool
	if strings.HasPrefix(s, "-") {
		neg = true
		s = s[1:]
	}
	var n int64
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errors.New("non-numeric timestamp")
		}
		n = n*10 + int64(c-'0')
	}
	if neg {
		n = -n
	}
	return n, nil
}

// TicketNumberFromSubject extracts a ticket number from a subject like
// "Re: [#1042] Cannot log in" or "[#1042] Cannot log in". Returns 0 if none.
func TicketNumberFromSubject(subject string) int64 {
	s := strings.TrimSpace(subject)
	for i := 0; i+3 < len(s); i++ {
		if s[i] != '[' {
			continue
		}
		end := strings.IndexByte(s[i+1:], ']')
		if end < 0 {
			continue
		}
		inner := strings.TrimSpace(s[i+1 : i+1+end])
		if !strings.HasPrefix(inner, "#") {
			continue
		}
		num, err := parseInt(inner[1:])
		if err == nil && num > 0 {
			return num
		}
	}
	return 0
}

func parseInt(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("empty")
	}
	var n int64
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errors.New("non-numeric")
		}
		n = n*10 + int64(c-'0')
	}
	return n, nil
}

// String makes log output readable.
func (e InboundEmail) String() string {
	return fmt.Sprintf("inbound <%s> from=%s subject=%q", e.MessageID, e.FromEmail, e.Subject)
}

func (e *InboundEmail) validate() error {
	if e.MessageID == "" {
		return ErrInvalidInput
	}
	if e.FromEmail == "" {
		return ErrInvalidInput
	}
	if e.Recipient == "" {
		return ErrInvalidInput
	}
	if e.Subject == "" {
		e.Subject = "(no subject)"
	}
	if e.Body == "" {
		e.Body = "(no body)"
	}
	return nil
}
