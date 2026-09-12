// Package algorithms provides concrete Algorithm implementations for
// the ratelimit package. It imports ratelimit for the Algorithm and
// Result types; ratelimit itself never imports this package — the
// client wires a concrete algorithm into a Limiter via ratelimit.New.
package algorithms

import (
	"sync"
	"time"

	ratelimit "github.com/const-nash/go-rate-limiter"
	"github.com/const-nash/go-rate-limiter/internal/validate"
)

// FixedWindowCounter allows up to limit requests per fixed-size
// window. It is the simplest rate-limiting algorithm: cheap to
// implement and reason about, at the cost of allowing up to 2x limit
// requests across a window boundary (a burst at the end of one
// window followed immediately by a burst at the start of the next).
type FixedWindowCounter struct {
	limit       int
	window      time.Duration
	count       int
	windowStart time.Time
	mu          sync.Mutex
}

func NewFixedWindowCounter(limit int, window time.Duration) *FixedWindowCounter {
	if err := validate.Limit(limit); err != nil {
		panic(err)
	}
	if err := validate.Window(window); err != nil {
		panic(err)
	}

	return &FixedWindowCounter{
		limit:  limit,
		window: window,
	}
}

func (f *FixedWindowCounter) Allow(now time.Time) ratelimit.Result {
	return f.AllowN(now, 1)
}

func (f *FixedWindowCounter) AllowN(now time.Time, n int) ratelimit.Result {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.advance(now)
	resetAt := f.windowStart.Add(f.window)

	if f.count+n > f.limit {
		return ratelimit.Result{
			Allowed:    false,
			Remaining:  f.limit - f.count,
			RetryAfter: resetAt.Sub(now),
			ResetAt:    resetAt,
			Limit:      f.limit,
		}
	}

	f.count += n

	return ratelimit.Result{
		Allowed:   true,
		Remaining: f.limit - f.count,
		ResetAt:   resetAt,
		Limit:     f.limit,
	}
}

// advance rolls windowStart forward to the window that contains now,
// resetting count, whenever the current window has elapsed. Windows
// are aligned to absolute time (via Truncate) rather than to the
// first request, so window boundaries are deterministic regardless
// of when the counter happens to receive its first call.
func (f *FixedWindowCounter) advance(now time.Time) {
	if now.Before(f.windowStart.Add(f.window)) {
		return
	}
	f.windowStart = now.Truncate(f.window)
	f.count = 0
}
