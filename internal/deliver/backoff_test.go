package deliver

import (
	"context"
	"testing"
	"time"
)

func TestCappedDelay(t *testing.T) {
	const min = time.Second
	const max = 5 * time.Minute
	cases := []struct {
		attempt uint
		want    time.Duration
	}{
		{0, 1 * time.Second},
		{1, 2 * time.Second},
		{2, 4 * time.Second},
		{3, 8 * time.Second},
		{8, 256 * time.Second},
		{9, 5 * time.Minute},   // 512s capped
		{10, 5 * time.Minute},  // stays capped
		{100, 5 * time.Minute}, // overflow-proof
	}
	for _, tc := range cases {
		if got := CappedDelay(tc.attempt, min, max); got != tc.want {
			t.Errorf("CappedDelay(%d) = %s, want %s", tc.attempt, got, tc.want)
		}
	}
}

func TestBackoffJitterBounds(t *testing.T) {
	b := NewBackoff(time.Second, 5*time.Minute)
	// Equal jitter: Next() in [d/2, d] for the uncapped core d.
	for i := 0; i < 50; i++ {
		b.Reset()
		got := b.Next()
		if got < 500*time.Millisecond || got > time.Second {
			t.Fatalf("attempt-0 delay %s outside [500ms, 1s]", got)
		}
		if b.Attempts() != 1 {
			t.Fatalf("attempts = %d, want 1", b.Attempts())
		}
	}
	// Deep streak saturates at the cap bounds.
	for i := 0; i < 20; i++ {
		b.Next()
	}
	for i := 0; i < 20; i++ {
		got := b.Next()
		if got < 150*time.Second || got > 5*time.Minute {
			t.Fatalf("saturated delay %s outside [150s, 5m]", got)
		}
	}
	b.Reset()
	if b.Attempts() != 0 {
		t.Fatalf("reset attempts = %d, want 0", b.Attempts())
	}
	if got := b.Next(); got < 500*time.Millisecond || got > time.Second {
		t.Fatalf("post-reset delay %s outside [500ms, 1s]", got)
	}
}

func TestBackoffWaitCancellation(t *testing.T) {
	b := NewBackoff(time.Hour, 2*time.Hour) // would sleep ~30-60m uncancelled
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if err := b.Wait(cancelled); err == nil {
		t.Fatal("Wait on cancelled ctx must return ctx error")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("Wait did not abort promptly on cancellation")
	}
}
