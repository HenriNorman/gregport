package webhooks

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Header names sent with every webhook request. Receivers verify
// X-Gregport-Signature as hex(HMAC-SHA256(secret, timestamp + "." + body)).
const (
	HeaderEvent     = "X-Gregport-Event"
	HeaderDelivery  = "X-Gregport-Delivery"
	HeaderTimestamp = "X-Gregport-Timestamp"
	HeaderSignature = "X-Gregport-Signature"
	HeaderAttempt   = "X-Gregport-Attempt"
)

const (
	// DefaultTimeout is the hard limit for one delivery attempt.
	DefaultTimeout = 10 * time.Second
	// DefaultFailureThreshold is how many consecutive attempts may fail before
	// a subscription is disabled and its admins are notified.
	DefaultFailureThreshold = 10
)

// SenderOptions configures the HTTP sender. AllowPrivate exists only for tests:
// production callers must leave it false so loopback/private destinations are
// refused at dial time.
type SenderOptions struct {
	Timeout      time.Duration
	AllowPrivate bool
}

// Response is the outcome of one HTTP attempt.
type Response struct {
	StatusCode int
	Body       []byte
	Duration   time.Duration
	Err        error
}

// Success reports whether the receiver accepted the delivery. Any 2xx is a
// success; redirects are not followed and count as failures.
func (r Response) Success() bool {
	return r.Err == nil && r.StatusCode >= 200 && r.StatusCode < 300
}

// Sender delivers one webhook request. The concrete implementation is
// HTTPSender; tests can substitute a fake.
type Sender interface {
	Send(ctx context.Context, sub Subscription, delivery Delivery) Response
}

// HTTPSender signs and POSTs webhook envelopes with a hardened HTTP client.
type HTTPSender struct {
	client *http.Client
}

// NewHTTPSender builds the production sender. Its dialer refuses private,
// loopback, link-local and metadata addresses even if DNS changes after the
// subscription was validated, and it does not follow redirects.
func NewHTTPSender(opts SenderOptions) *HTTPSender {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}

	dialer := &net.Dialer{
		Timeout:   5 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	if !opts.AllowPrivate {
		dialer.Control = blockedDialControl
	}

	transport := &http.Transport{
		Proxy:                 nil, // never use HTTP(S)_PROXY: it would bypass the dialer check
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: timeout,
		MaxIdleConns:          100,
		IdleConnTimeout:       30 * time.Second,
	}
	return &HTTPSender{client: &http.Client{
		Timeout:   timeout,
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
}

// Send signs and delivers one envelope. Transport errors are reported in
// Response.Err; non-2xx statuses are ordinary responses whose Success method is
// false.
func (s *HTTPSender) Send(ctx context.Context, sub Subscription, delivery Delivery) Response {
	start := time.Now()
	body := []byte(delivery.Payload)
	if len(body) == 0 {
		body = []byte(`{}`)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.URL, bytes.NewReader(body))
	if err != nil {
		return Response{Duration: time.Since(start), Err: fmt.Errorf("build webhook request: %w", err)}
	}

	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Gregport-Webhook/1.0")
	req.Header.Set(HeaderEvent, delivery.EventType)
	req.Header.Set(HeaderDelivery, delivery.ID.String())
	req.Header.Set(HeaderTimestamp, timestamp)
	req.Header.Set(HeaderSignature, Sign(sub.Secret, timestamp, body))
	req.Header.Set(HeaderAttempt, strconv.Itoa(delivery.Attempts+1))

	resp, err := s.client.Do(req)
	if err != nil {
		return Response{Duration: time.Since(start), Err: fmt.Errorf("send webhook: %w", err)}
	}
	defer resp.Body.Close()

	// Read a bounded amount: a receiver must not be able to exhaust worker
	// memory by returning a huge body.
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBodyBytes*16))
	if readErr != nil {
		return Response{
			StatusCode: resp.StatusCode,
			Duration:   time.Since(start),
			Err:        fmt.Errorf("read webhook response: %w", readErr),
		}
	}
	return Response{
		StatusCode: resp.StatusCode,
		Body:       raw,
		Duration:   time.Since(start),
	}
}

// Sign computes hex(HMAC-SHA256(secret, timestamp + "." + body)).
func Sign(secret, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// HmacEnabled reports whether signing is configured. A stored subscription
// always has a secret; this guard keeps a bad row from producing an unsigned
// request.
func HmacEnabled(secret string) bool {
	return strings.TrimSpace(secret) != ""
}
