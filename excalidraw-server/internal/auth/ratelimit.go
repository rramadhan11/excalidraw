package auth

import (
	"sync"
	"time"
)

// Limiter counts failures per key in a fixed window. State is in-memory, which
// is enough for a single server instance.
type Limiter struct {
	mu      sync.Mutex
	max     int
	window  time.Duration
	entries map[string]*limiterEntry
	now     func() time.Time
}

type limiterEntry struct {
	failures int
	resetAt  time.Time
}

// sweepThreshold bounds memory: past this many keys, expired ones are dropped.
const sweepThreshold = 10_000

func NewLimiter(max int, window time.Duration) *Limiter {
	return &Limiter{
		max:     max,
		window:  window,
		entries: make(map[string]*limiterEntry),
		now:     time.Now,
	}
}

// Allow reports whether key is still under the failure limit.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.entries[key]
	if !ok || !l.now().Before(e.resetAt) {
		return true
	}
	return e.failures < l.max
}

func (l *Limiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	e, ok := l.entries[key]
	if !ok || !now.Before(e.resetAt) {
		if len(l.entries) >= sweepThreshold {
			l.sweep(now)
		}
		e = &limiterEntry{resetAt: now.Add(l.window)}
		l.entries[key] = e
	}
	e.failures++
}

func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, key)
}

func (l *Limiter) sweep(now time.Time) {
	for key, e := range l.entries {
		if !now.Before(e.resetAt) {
			delete(l.entries, key)
		}
	}
}
