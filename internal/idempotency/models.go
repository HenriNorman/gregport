// Package idempotency persists idempotency keys and their captured responses
// in PostgreSQL. Records are tenant-scoped: the same client key used by two
// organizations never collides.
package idempotency

import (
	"errors"
	"time"
)

const (
	// MaxKeyLength matches the conventional Idempotency-Key header limit.
	MaxKeyLength = 255
	// MaxRequestHashLength covers a hex SHA-256 plus room for a version tag.
	MaxRequestHashLength = 128
)

// Outcome describes what Acquire found for a key.
type Outcome int

const (
	// OutcomeAcquired means the caller owns the key and must produce the
	// response, then call Complete.
	OutcomeAcquired Outcome = iota
	// OutcomeReplay means a completed response exists and must be returned
	// unchanged.
	OutcomeReplay
	// OutcomePending means another request with the same key is still in
	// flight.
	OutcomePending
	// OutcomeConflict means the key was already used with a different request
	// hash, or the stored record is not usable.
	OutcomeConflict
)

func (o Outcome) String() string {
	switch o {
	case OutcomeAcquired:
		return "acquired"
	case OutcomeReplay:
		return "replay"
	case OutcomePending:
		return "pending"
	case OutcomeConflict:
		return "conflict"
	default:
		return "unknown"
	}
}

// Record is one stored idempotency key. ResponseStatus is zero while the
// request is still in flight.
type Record struct {
	RequestHash    string
	ResponseStatus int
	ResponseBody   []byte
	CreatedAt      time.Time
	ExpiresAt      time.Time
}

// Completed reports whether a response has been captured for the record.
func (r Record) Completed() bool { return r.ResponseStatus != 0 }

// AcquireResult is returned by Acquire.
type AcquireResult struct {
	Outcome Outcome
	Record  Record
}

var (
	// ErrInvalidInput rejects malformed keys, hashes, TTLs, or responses before
	// they reach PostgreSQL.
	ErrInvalidInput = errors.New("idempotency: invalid input")
	// ErrNotPending means Complete or Release found no matching in-flight
	// record. The record may be missing, expired, completed, or owned by a
	// different request hash.
	ErrNotPending = errors.New("idempotency: no matching pending record")
)
