// Package algorithms provides concrete Algorithm implementations for
// the ratelimit package. It imports ratelimit for the Algorithm and
// Result types; ratelimit itself never imports this package — the
// client wires a concrete algorithm into a Limiter via ratelimit.New.
package algorithms

import (
	"context"
	"sync"
	"time"

	ratelimit "github.com/const-nash/go-rate-limiter"
	"github.com/const-nash/go-rate-limiter/internal/validate"
)

// fixedWindowState is the state FixedWindowCounter keeps for one key.
type fixedWindowState struct {
	count       int
	windowStart time.Time
}

// FixedWindowCounter allows up to limit requests per fixed-size
// window, tracked independently per key. It is the simplest
// rate-limiting algorithm: cheap to implement and reason about, at
// the cost of allowing up to 2x limit requests across a window
// boundary (a burst at the end of one window followed immediately by
// a burst at the start of the next).
type FixedWindowCounter struct {
	limit  int
	window time.Duration
	store  ratelimit.KeyStore
	// mu serializes AllowN across every key on this instance, not just
	// the key being updated — simple and correct, at the cost of keys
	// contending with each other under concurrent traffic. That cost
	// grows sharply if store is backed by a network service, since
	// the whole critical section (including the round trip) runs
	// while mu is held.
	mu sync.Mutex
}

func NewFixedWindowCounter(limit int, window time.Duration, store ratelimit.KeyStore) *FixedWindowCounter {
	if err := validate.Limit(limit); err != nil {
		panic(err)
	}
	if err := validate.Window(window); err != nil {
		panic(err)
	}

	return &FixedWindowCounter{
		limit:  limit,
		window: window,
		store:  store,
	}
}

func (f *FixedWindowCounter) Allow(ctx context.Context, now time.Time, key string) (ratelimit.Result, error) {
	return f.AllowN(ctx, now, key, 1)
}

func (f *FixedWindowCounter) AllowN(ctx context.Context, now time.Time, key string, n int) (ratelimit.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	state, err := f.loadState(ctx, key)
	if err != nil {
		return ratelimit.Result{}, err
	}

	f.advance(&state, now)
	resetAt := state.windowStart.Add(f.window)

	if state.count+n > f.limit {
		if err := f.store.Set(ctx, key, state); err != nil {
			return ratelimit.Result{}, err
		}
		return ratelimit.Result{
			Allowed:    false,
			Remaining:  f.limit - state.count,
			RetryAfter: resetAt.Sub(now),
			ResetAt:    resetAt,
			Limit:      f.limit,
		}, nil
	}

	state.count += n
	if err := f.store.Set(ctx, key, state); err != nil {
		return ratelimit.Result{}, err
	}

	return ratelimit.Result{
		Allowed:   true,
		Remaining: f.limit - state.count,
		ResetAt:   resetAt,
		Limit:     f.limit,
	}, nil
}

func (f *FixedWindowCounter) loadState(ctx context.Context, key string) (fixedWindowState, error) {
	raw, ok, err := f.store.Get(ctx, key)
	if err != nil {
		return fixedWindowState{}, err
	}
	if !ok {
		return fixedWindowState{}, nil
	}
	state, ok := raw.(fixedWindowState)
	if !ok {
		return fixedWindowState{}, nil
	}
	return state, nil
}

// advance rolls state.windowStart forward to the window that contains
// now, resetting count, whenever the current window has elapsed.
// Windows are aligned to absolute time (via Truncate) rather than to
// the key's first request, so window boundaries are deterministic
// regardless of when a given key happens to make its first call.
func (f *FixedWindowCounter) advance(state *fixedWindowState, now time.Time) {
	windowEnd := state.windowStart.Add(f.window)
	if now.Before(windowEnd) {
		return
	}
	state.windowStart = now.Truncate(f.window)
	state.count = 0
}
