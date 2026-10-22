package reconciler

import (
	"testing"
	"time"
)

func TestBackoffGrowsCapsAndJitters(t *testing.T) {
	mid := func() float64 { return 0.5 } // factor 1.0
	want := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 32 * time.Second, 64 * time.Second, 128 * time.Second, 256 * time.Second, 5 * time.Minute, 5 * time.Minute}
	for i, w := range want {
		if got := Backoff(i+1, mid); got != w {
			t.Errorf("attempt %d: got %s want %s", i+1, got, w)
		}
	}
	if lo, hi := Backoff(1, func() float64 { return 0 }), Backoff(1, func() float64 { return 1 }); lo != 1600*time.Millisecond || hi != 2400*time.Millisecond {
		t.Errorf("jitter bounds %s..%s, want 1.6s..2.4s", lo, hi)
	}
	if Backoff(0, mid) != 2*time.Second {
		t.Error("attempt 0 should behave like 1")
	}
}
