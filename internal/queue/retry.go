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

// The webhook schedule is intentionally slower than the generic task
// backoff: receivers are often third-party services that need minutes, not
// seconds, to recover. Six retries after the initial attempt span roughly one
// day: 1m, 5m, 30m, 2h, 6h, 12h.
var webhookBackoff = []time.Duration{
	time.Minute,
	5 * time.Minute,
	30 * time.Minute,
	2 * time.Hour,
	6 * time.Hour,
	12 * time.Hour,
}

// WebhookMaxAttempts is the total number of HTTP attempts (initial plus
// retries) before a delivery is marked dead.
func WebhookMaxAttempts() int { return len(webhookBackoff) + 1 }

// WebhookMaxRetries is the asynq MaxRetry value for a delivery task.
func WebhookMaxRetries() int { return len(webhookBackoff) }

// WebhookRetryDelay returns the delay before the next attempt after
// failedAttempts failures (1-based). A terminal attempt returns zero.
func WebhookRetryDelay(failedAttempts int) time.Duration {
	if failedAttempts < 1 || failedAttempts > len(webhookBackoff) {
		return 0
	}
	return webhookBackoff[failedAttempts-1]
}

// RetryDelayForTask applies the generic backoff to ordinary tasks and the
// webhook schedule to webhook delivery tasks. asynq passes the number of
// retries already performed, which is one less than the failed attempt count.
func RetryDelayForTask(attempt int, err error, task *asynq.Task) time.Duration {
	if task != nil && task.Type() == TaskWebhookDeliver {
		return WebhookRetryDelay(attempt + 1)
	}
	return RetryDelay(attempt, err, task)
}
