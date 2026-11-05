package queue

import (
	"testing"
	"time"
)

func TestBackoffSchedule(t *testing.T) {
	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{0, 5 * time.Second},
		{1, 5 * time.Second},
		{2, 10 * time.Second},
		{3, 20 * time.Second},
		{4, 40 * time.Second},
		{5, 80 * time.Second},
		{6, 160 * time.Second},
		{7, 320 * time.Second},
		{8, 640 * time.Second},
		{9, 1280 * time.Second},
		{10, 2560 * time.Second},
		{11, time.Hour},
		{20, time.Hour},
		{100, time.Hour},
		{-5, 5 * time.Second},
	}
	for _, tt := range tests {
		if got := Backoff(tt.attempt); got != tt.want {
			t.Fatalf("Backoff(%d) = %s, want %s", tt.attempt, got, tt.want)
		}
	}
}

func TestRetryDelayJitterBounds(t *testing.T) {
	for attempt := 1; attempt <= 12; attempt++ {
		base := Backoff(attempt)
		delta := base / 5
		min := base - delta
		max := base + delta

		for i := 0; i < 200; i++ {
			got := RetryDelay(attempt, nil, nil)
			if got < min || got > max {
				t.Fatalf("RetryDelay(%d) = %s outside [%s, %s]", attempt, got, min, max)
			}
		}
	}
}

func TestRetryDelayJitters(t *testing.T) {
	seen := make(map[time.Duration]bool)
	for i := 0; i < 50; i++ {
		seen[RetryDelay(4, nil, nil)] = true
	}
	if len(seen) < 2 {
		t.Fatal("retry delays should vary between attempts")
	}
}
