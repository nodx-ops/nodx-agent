// Package deliver posts self-capped batches to the ingestion edge with a
// fsync'd file spool for network partitions.
//
// Retry discipline: 429/5xx/timeouts spool + backoff; 4xx (non-429) drop +
// counter; 401/403 is ErrAuthFatal (operator error: log.Fatal, never looped).
package deliver

import (
	"context"
	"math/rand/v2"
	"time"
)

const (
	// DefaultBackoffMin/Max bound the reconnect delay: 1s → 5min + jitter.
	DefaultBackoffMin = time.Second
	DefaultBackoffMax = 5 * time.Minute
)

// CappedDelay is the pure exponential core: min*2^attempt, capped at max.
// Jitter-free so it is exactly table-testable; Next adds the jitter.
func CappedDelay(attempt uint, min, max time.Duration) time.Duration {
	d := min
	for i := uint(0); i < attempt; i++ {
		d *= 2
		if d >= max {
			return max
		}
	}
	if d > max {
		return max
	}
	return d
}

// Backoff tracks consecutive retryable failures. Reset on any success.
type Backoff struct {
	min, max time.Duration
	attempts uint
}

// NewBackoff builds the delivery backoff (1s → 5min).
func NewBackoff(min, max time.Duration) *Backoff {
	return &Backoff{min: min, max: max}
}

// Attempts reports consecutive failures (for observability/tests).
func (b *Backoff) Attempts() uint { return b.attempts }

// Reset clears the failure streak after a success.
func (b *Backoff) Reset() { b.attempts = 0 }

// Next returns the next delay: capped exponential with equal jitter
// (d/2 + [0, d/2]) so the fleet does not thundering-herd the edge.
func (b *Backoff) Next() time.Duration {
	d := CappedDelay(b.attempts, b.min, b.max)
	b.attempts++
	half := int64(d / 2)
	if half <= 0 {
		return d
	}
	return time.Duration(half + rand.Int64N(half+1))
}

// Wait sleeps for the next delay, aborting early on ctx cancellation.
// Never time.After-in-loop: the caller owns the timer lifecycle here.
func (b *Backoff) Wait(ctx context.Context) error {
	t := time.NewTimer(b.Next())
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
