package queue

import (
	"math/rand/v2"
	"time"

	"github.com/hibiken/asynq"
)

const (
	retryBase = 5 * time.Second
	retryMax  = time.Hour
)

// Backoff returns the deterministic exponential delay for a 1-based attempt:
// 5s, 10s, 20s, ... capped at one hour. Jitter is applied separately by
// RetryDelay so the schedule itself stays easy to reason about and test.
func Backoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 20 {
		return retryMax
	}

	d := retryBase << (attempt - 1)
	if d <= 0 || d > retryMax {
		return retryMax
	}
	return d
}

// RetryDelay applies ±20% jitter to Backoff so retries from many workers do
// not arrive in synchronized waves.
func RetryDelay(attempt int, _ error, _ *asynq.Task) time.Duration {
	d := Backoff(attempt)
	delta := int64(d) / 5
	if delta <= 0 {
		return d
	}
	return time.Duration(int64(d) - delta + rand.Int64N(2*delta+1))
}
