package email

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/sony/gobreaker"
)

// Sender sends a single email. Implementations may be SMTP, a third-party API,
// or a test double. Send must be safe for concurrent use.
type Sender interface {
	// Send delivers one email and returns nil on success. A non-nil error is
	// treated as a delivery failure and counted against the circuit breaker.
	Send(ctx context.Context, to, template string, payload DeliveryPayload) error
}

// CircuitBreaker wraps a Sender with gobreaker so a failing mail provider
// cannot exhaust retries forever. When the circuit opens, Send returns the
// breaker's error immediately without calling the underlying sender. After the
// timeout the breaker goes half-open and admits a single probe request.
type CircuitBreaker struct {
	inner Sender
	cb    *gobreaker.CircuitBreaker
}

// NewCircuitBreaker wraps sender. maxConsecutiveFailures trips the breaker;
// resetTimeout is how long it stays open before a half-open probe.
func NewCircuitBreaker(inner Sender, maxConsecutiveFailures uint32, resetTimeout time.Duration) *CircuitBreaker {
	settings := gobreaker.Settings{
		Name:        "email",
		MaxRequests: 1, // one probe at a time while half-open
		Interval:    0, // disable the metrics window; we trip on consecutive failures
		Timeout:     resetTimeout,
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			return counts.ConsecutiveFailures >= maxConsecutiveFailures
		},
		OnStateChange: func(name string, from gobreaker.State, to gobreaker.State) {
			// Callers can log state changes through a wrapped logger if desired.
		},
		IsSuccessful: func(err error) bool {
			return err == nil
		},
	}
	return &CircuitBreaker{inner: inner, cb: gobreaker.NewCircuitBreaker(settings)}
}

// Send delivers through the circuit breaker.
func (c *CircuitBreaker) Send(ctx context.Context, to, template string, payload DeliveryPayload) error {
	_, err := c.cb.Execute(func() (interface{}, error) {
		return nil, c.inner.Send(ctx, to, template, payload)
	})
	if err != nil {
		return fmt.Errorf("circuit breaker: %w", err)
	}
	return nil
}

// State returns the current circuit breaker state.
func (c *CircuitBreaker) State() gobreaker.State {
	return c.cb.State()
}

// Counts returns the breaker's failure/success counts.
func (c *CircuitBreaker) Counts() gobreaker.Counts {
	return c.cb.Counts()
}

// ErrCircuitOpen is returned when gobreaker rejects a call because the circuit
// is open. gobreaker returns ErrOpenState directly; this sentinel lets callers
// wrap or re-export it consistently.
var ErrCircuitOpen = errors.New("email: circuit breaker open")

// IsCircuitOpen reports whether err indicates the circuit is open.
func IsCircuitOpen(err error) bool {
	return errors.Is(err, ErrCircuitOpen) || errors.Is(err, gobreaker.ErrOpenState)
}
