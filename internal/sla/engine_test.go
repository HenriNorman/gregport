package sla

import (
	"errors"
	"testing"
	"time"
)

func TestWallClockDeadlines(t *testing.T) {
	created := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)

	tests := []struct {
		name    string
		target  DeadlineTarget
		wantFR  time.Time
		wantRes time.Time
		wantErr error
	}{
		{
			name:    "urgent policy",
			target:  DeadlineTarget{FirstResponseMinutes: 15, ResolutionMinutes: 120},
			wantFR:  created.Add(15 * time.Minute),
			wantRes: created.Add(120 * time.Minute),
		},
		{
			name:    "one minute each",
			target:  DeadlineTarget{FirstResponseMinutes: 1, ResolutionMinutes: 1},
			wantFR:  created.Add(time.Minute),
			wantRes: created.Add(time.Minute),
		},
		{
			name:    "large window",
			target:  DeadlineTarget{FirstResponseMinutes: 60 * 24 * 30, ResolutionMinutes: 60 * 24 * 90},
			wantFR:  created.Add(30 * 24 * time.Hour),
			wantRes: created.Add(90 * 24 * time.Hour),
		},
		{
			name:    "zero first response",
			target:  DeadlineTarget{FirstResponseMinutes: 0, ResolutionMinutes: 60},
			wantErr: ErrInvalidDeadline,
		},
		{
			name:    "zero resolution",
			target:  DeadlineTarget{FirstResponseMinutes: 15, ResolutionMinutes: 0},
			wantErr: ErrInvalidDeadline,
		},
		{
			name:    "negative first response",
			target:  DeadlineTarget{FirstResponseMinutes: -1, ResolutionMinutes: 60},
			wantErr: ErrInvalidDeadline,
		},
		{
			name:    "negative resolution",
			target:  DeadlineTarget{FirstResponseMinutes: 15, ResolutionMinutes: -1},
			wantErr: ErrInvalidDeadline,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := WallClock{}.Deadlines(created, tt.target)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if !got.FirstResponse.Equal(tt.wantFR) {
				t.Fatalf("first response = %s, want %s", got.FirstResponse, tt.wantFR)
			}
			if !got.Resolution.Equal(tt.wantRes) {
				t.Fatalf("resolution = %s, want %s", got.Resolution, tt.wantRes)
			}
		})
	}
}

func TestWallClockDeadlinesPreserveInstant(t *testing.T) {
	loc := time.FixedZone("EAT", 3*60*60)
	created := time.Date(2026, 1, 15, 10, 0, 0, 0, loc)

	got, err := WallClock{}.Deadlines(created, DeadlineTarget{
		FirstResponseMinutes: 15,
		ResolutionMinutes:    120,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !got.FirstResponse.Equal(created.Add(15 * time.Minute)) {
		t.Fatalf("first response shifted instant: %s", got.FirstResponse)
	}
	if !got.Resolution.Equal(created.Add(120 * time.Minute)) {
		t.Fatalf("resolution shifted instant: %s", got.Resolution)
	}
}

func TestDeadlineTargetFromPolicy(t *testing.T) {
	p := Policy{FirstResponseMinutes: 30, ResolutionMinutes: 240}
	got := TargetFromPolicy(p)
	if got.FirstResponseMinutes != 30 || got.ResolutionMinutes != 240 {
		t.Fatalf("target = %+v", got)
	}
}

func TestWallClockSatisfiesCalculator(t *testing.T) {
	var _ Calculator = WallClock{}
}
