package auth

import (
	"sync"
	"time"
)

// Throttle limits failed sign-ins per key (email + client IP): Max failures inside Window block
// further attempts until the oldest failure leaves the window (ADR-007).
type Throttle struct {
	Max    int
	Window time.Duration
	now    func() time.Time

	mu       sync.Mutex
	failures map[string][]time.Time
}

// NewThrottle builds a throttle with the given clock (time.Now in production).
func NewThrottle(max int, window time.Duration, now func() time.Time) *Throttle {
	return &Throttle{Max: max, Window: window, now: now, failures: map[string][]time.Time{}}
}

// Blocked reports whether key is throttled and, if so, how long until it is not.
func (t *Throttle) Blocked(key string) (bool, time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	recent := t.prune(key)
	if len(recent) < t.Max {
		return false, 0
	}
	return true, recent[0].Add(t.Window).Sub(t.now())
}

// Fail records a failed attempt.
func (t *Throttle) Fail(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.failures[key] = append(t.prune(key), t.now())
}

// Succeed clears a key's failures.
func (t *Throttle) Succeed(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.failures, key)
}

func (t *Throttle) prune(key string) []time.Time {
	cutoff := t.now().Add(-t.Window)
	kept := t.failures[key][:0]
	for _, at := range t.failures[key] {
		if at.After(cutoff) {
			kept = append(kept, at)
		}
	}
	if len(kept) == 0 {
		delete(t.failures, key)
		return nil
	}
	t.failures[key] = kept
	return kept
}
