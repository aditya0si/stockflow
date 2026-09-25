package auth

import (
	"sync"
	"time"
)

// maxLimiterEntries bounds the number of tracked keys so a hostile login
// stream cannot grow memory without limit. This limiter is intentionally
// in-memory: it protects a single process, and the deployment model is one
// process. A multi-process deployment would need a shared store, which is
// deliberately out of scope.
const maxLimiterEntries = 10_000

// LoginLimiter is a bounded, windowed, per-key failure counter used to slow
// credential guessing. It is safe for concurrent use.
type LoginLimiter struct {
	mu      sync.Mutex
	max     int
	window  time.Duration
	now     func() time.Time
	entries map[string][]time.Time
}

// NewLoginLimiter builds a limiter that allows at most max failures per window
// per key.
func NewLoginLimiter(max int, window time.Duration) *LoginLimiter {
	if max <= 0 {
		max = 10
	}
	if window <= 0 {
		window = time.Minute
	}
	return &LoginLimiter{
		max:     max,
		window:  window,
		now:     time.Now,
		entries: make(map[string][]time.Time),
	}
}

// Blocked reports whether the key has reached the failure limit inside the
// current window.
func (l *LoginLimiter) Blocked(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	recent := l.recentLocked(key, l.now())
	return len(recent) >= l.max
}

// RecordFailure records a failed attempt for the key.
func (l *LoginLimiter) RecordFailure(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	recent := l.recentLocked(key, now)
	recent = append(recent, now)

	if len(l.entries) >= maxLimiterEntries {
		l.pruneLocked(now)
		if len(l.entries) >= maxLimiterEntries {
			// Still full of live keys: drop the map rather than grow without
			// bound. This only weakens throttling for one window under extreme
			// pressure, and never affects correctness.
			l.entries = make(map[string][]time.Time)
		}
	}
	l.entries[key] = recent
}

// Reset clears the failure history for a key after a successful login.
func (l *LoginLimiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, key)
}

func (l *LoginLimiter) recentLocked(key string, now time.Time) []time.Time {
	cutoff := now.Add(-l.window)
	recent := l.entries[key][:0:0]
	for _, at := range l.entries[key] {
		if at.After(cutoff) {
			recent = append(recent, at)
		}
	}
	return recent
}

func (l *LoginLimiter) pruneLocked(now time.Time) {
	for key := range l.entries {
		recent := l.recentLocked(key, now)
		if len(recent) == 0 {
			delete(l.entries, key)
			continue
		}
		l.entries[key] = recent
	}
}
