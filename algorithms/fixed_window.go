// Package algorithms provides concrete Algorithm implementations for
// the ratelimit package. It imports ratelimit for the Algorithm and
// Result types; ratelimit itself never imports this package — the
// client wires a concrete algorithm into a Limiter via ratelimit.New.
package algorithms

import (
	"context"
	"time"

	ratelimit "github.com/const-nash/go-rate-limiter"
	"github.com/const-nash/go-rate-limiter/internal/validate"
)

// FixedWindowState is the state FixedWindowCounter keeps for one key.
//
// Its fields are deliberately unexported: the type is exported only
// so a caller can name it when building the store that holds it
// (ratelimit.NewMapStore[algorithms.FixedWindowState]()). Its zero
// value is a key that has not been seen yet.
type FixedWindowState struct {
	count       int
	windowStart time.Time
}

// FixedWindowCounter allows up to limit requests per fixed-size
// window, tracked independently per key. It is the simplest
// rate-limiting algorithm: cheap to implement and reason about, at
// the cost of allowing up to 2x limit requests across a window
// boundary (a burst at the end of one window followed immediately by
// a burst at the start of the next).
//
// It holds no lock of its own: read-modify-write for one key happens
// inside a single store.Update, so whatever the KeyStore uses to make
// that atomic (MapStore takes one shard lock) is the only
// serialization, and keys that don't collide there proceed in
// parallel.
type FixedWindowCounter struct {
	limit  int
	window time.Duration
	store  ratelimit.KeyStore[FixedWindowState]
}

func NewFixedWindowCounter(limit int, window time.Duration, store ratelimit.KeyStore[FixedWindowState]) *FixedWindowCounter {
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
	return f.store.Update(ctx, key, func(state FixedWindowState, _ bool) (FixedWindowState, ratelimit.Result, error) {
		f.advance(&state, now)
		resetAt := state.windowStart.Add(f.window)

		if state.count+n > f.limit {
			// Still written back: advance may have rolled the window over.
			return state, ratelimit.Result{
				Allowed:    false,
				Remaining:  f.limit - state.count,
				RetryAfter: resetAt.Sub(now),
				ResetAt:    resetAt,
				Limit:      f.limit,
			}, nil
		}

		state.count += n
		return state, ratelimit.Result{
			Allowed:   true,
			Remaining: f.limit - state.count,
			ResetAt:   resetAt,
			Limit:     f.limit,
		}, nil
	})
}

// advance rolls state.windowStart forward to the window that contains
// now, resetting count, whenever the current window has elapsed.
// Windows are aligned to absolute time (via Truncate) rather than to
// the key's first request, so window boundaries are deterministic
// regardless of when a given key happens to make its first call.
func (f *FixedWindowCounter) advance(state *FixedWindowState, now time.Time) {
	windowEnd := state.windowStart.Add(f.window)
	if now.Before(windowEnd) {
		return
	}
	state.windowStart = now.Truncate(f.window)
	state.count = 0
}
