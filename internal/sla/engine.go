package sla

import (
	"errors"
	"time"
)

// ErrInvalidDeadline is returned when a policy has non-positive targets.
var ErrInvalidDeadline = errors.New("sla: deadline minutes must be positive")

// DeadlineTarget is the part of a policy the calculator needs. Keeping it
// separate from Policy lets a business-hours calendar replace the wall-clock
// calculation without touching the policy store.
type DeadlineTarget struct {
	FirstResponseMinutes int
	ResolutionMinutes    int
}

// Deadlines are the absolute instants a ticket must be answered and resolved
// by. Both are derived from the ticket's creation time.
type Deadlines struct {
	FirstResponse time.Time
	Resolution    time.Time
}

// Calculator turns a creation time and a target into deadlines. Version one
// is WallClock; a future Calendar implementation can skip nights, weekends and
// holidays behind the same signature.
type Calculator interface {
	Deadlines(createdAt time.Time, target DeadlineTarget) (Deadlines, error)
}

// WallClock computes deadlines by adding calendar minutes. It is pure: the
// same inputs always produce the same outputs.
type WallClock struct{}

// Deadlines implements Calculator.
func (WallClock) Deadlines(createdAt time.Time, target DeadlineTarget) (Deadlines, error) {
	if target.FirstResponseMinutes <= 0 || target.ResolutionMinutes <= 0 {
		return Deadlines{}, ErrInvalidDeadline
	}
	return Deadlines{
		FirstResponse: createdAt.Add(time.Duration(target.FirstResponseMinutes) * time.Minute),
		Resolution:    createdAt.Add(time.Duration(target.ResolutionMinutes) * time.Minute),
	}, nil
}

// TargetFromPolicy projects a stored policy onto the calculator input.
func TargetFromPolicy(p Policy) DeadlineTarget {
	return DeadlineTarget{
		FirstResponseMinutes: p.FirstResponseMinutes,
		ResolutionMinutes:    p.ResolutionMinutes,
	}
}
