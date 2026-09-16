package algorithms

import (
	"context"
	"time"

	ratelimit "github.com/const-nash/go-rate-limiter"
	"github.com/const-nash/go-rate-limiter/internal/validate"
)

// SlidingWindowState is the state SlidingWindowLog keeps for one key:
// the timestamp of every request still inside the trailing window,
// oldest first.
//
// Its field is deliberately unexported: the type is exported only so
// a caller can name it when building the store that holds it
// (ratelimit.NewMapStore[algorithms.SlidingWindowState]()). Its zero
// value is a key that has not been seen yet.
type SlidingWindowState struct {
	timestamps []time.Time
}

// SlidingWindowLog allows up to limit requests within any trailing
// window of the given duration, tracked independently per key. Unlike
// FixedWindowCounter, it keeps a timestamp per accepted request
// instead of a single counter, and on every call discards whichever
// timestamps have aged out of [now-window, now) before counting the
// rest. That gives an exact sliding window — no boundary burst — at
// the cost of O(limit) memory per key instead of FixedWindowCounter's
// O(1).
//
// It holds no lock of its own: read-modify-write for one key happens
// inside a single store.Update, so whatever the KeyStore uses to make
// that atomic (MapStore takes one shard lock) is the only
// serialization, and keys that don't collide there proceed in
// parallel.
type SlidingWindowLog struct {
	limit  int
	window time.Duration
	store  ratelimit.KeyStore[SlidingWindowState]
}

func NewSlidingWindowLog(limit int, window time.Duration, store ratelimit.KeyStore[SlidingWindowState]) *SlidingWindowLog {
	if err := validate.Limit(limit); err != nil {
		panic(err)
	}
	if err := validate.Window(window); err != nil {
		panic(err)
	}

	return &SlidingWindowLog{
		limit:  limit,
		window: window,
		store:  store,
	}
}

func (s *SlidingWindowLog) Allow(ctx context.Context, now time.Time, key string) (ratelimit.Result, error) {
	return s.AllowN(ctx, now, key, 1)
}

func (s *SlidingWindowLog) AllowN(ctx context.Context, now time.Time, key string, n int) (ratelimit.Result, error) {
	return s.store.Update(ctx, key, func(state SlidingWindowState, _ bool) (SlidingWindowState, ratelimit.Result, error) {
		state.timestamps = s.evict(state.timestamps, now)

		if len(state.timestamps)+n > s.limit {
			resetAt := s.resetAt(state.timestamps, now)
			// Still written back: evict may have dropped aged-out entries.
			return state, ratelimit.Result{
				Allowed:    false,
				Remaining:  s.limit - len(state.timestamps),
				RetryAfter: resetAt.Sub(now),
				ResetAt:    resetAt,
				Limit:      s.limit,
			}, nil
		}

		for i := 0; i < n; i++ {
			state.timestamps = append(state.timestamps, now)
		}
		return state, ratelimit.Result{
			Allowed:   true,
			Remaining: s.limit - len(state.timestamps),
			ResetAt:   s.resetAt(state.timestamps, now),
			Limit:     s.limit,
		}, nil
	})
}

// evict drops every timestamp that has aged out of the trailing
// window. Timestamps are always appended in non-decreasing order, so
// the survivors are a suffix and a single forward scan finds them.
func (s *SlidingWindowLog) evict(timestamps []time.Time, now time.Time) []time.Time {
	threshold := now.Add(-s.window)
	i := 0
	for i < len(timestamps) && !timestamps[i].After(threshold) {
		i++
	}
	return timestamps[i:]
}

// resetAt is when the oldest recorded request ages out of the
// window, freeing the next slot. With no recorded requests the
// window already has room, so it resolves to now.
func (s *SlidingWindowLog) resetAt(timestamps []time.Time, now time.Time) time.Time {
	if len(timestamps) == 0 {
		return now
	}
	return timestamps[0].Add(s.window)
}
