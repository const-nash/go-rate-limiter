package algorithms

import (
	"context"
	"time"

	ratelimit "github.com/const-nash/go-rate-limiter"
	"github.com/const-nash/go-rate-limiter/internal/validate"
)

// SlidingWindowState is the state SlidingWindowLog keeps for one key:
// the timestamp, in nanoseconds since the Unix epoch, of every request
// still inside the trailing window, oldest first.
//
// Its field is deliberately unexported: the type is exported only so
// a caller can name it when building the store that holds it
// (ratelimit.NewMapStore[algorithms.SlidingWindowState]()). Its zero
// value is a key that has not been seen yet.
type SlidingWindowState struct {
	timestamps []int64
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

func (s *SlidingWindowLog) Allow(ctx context.Context, now int64, key string) (ratelimit.Result, error) {
	return s.AllowN(ctx, now, key, 1)
}

func (s *SlidingWindowLog) AllowN(ctx context.Context, now int64, key string, n int) (ratelimit.Result, error) {
	return s.store.Update(ctx, now, key, func(state SlidingWindowState, _ bool) (SlidingWindowState, int64, ratelimit.Result, error) {
		state.timestamps = s.evict(state.timestamps, now)

		if len(state.timestamps)+n > s.limit {
			resetAt := s.resetAt(state.timestamps, now)
			// Still written back: evict may have dropped aged-out entries.
			return state, s.expiresAt(state.timestamps, now), ratelimit.Result{
				Allowed:    false,
				Remaining:  s.limit - len(state.timestamps),
				RetryAfter: time.Duration(resetAt - now),
				ResetAt:    resetAt,
				Limit:      s.limit,
			}, nil
		}

		for i := 0; i < n; i++ {
			state.timestamps = append(state.timestamps, now)
		}
		return state, s.expiresAt(state.timestamps, now), ratelimit.Result{
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
func (s *SlidingWindowLog) evict(timestamps []int64, now int64) []int64 {
	threshold := now - int64(s.window)
	i := 0
	for i < len(timestamps) && timestamps[i] <= threshold {
		i++
	}
	return timestamps[i:]
}

// resetAt is when the oldest recorded request ages out of the
// window, freeing the next slot. With no recorded requests the
// window already has room, so it resolves to now.
func (s *SlidingWindowLog) resetAt(timestamps []int64, now int64) int64 {
	if len(timestamps) == 0 {
		return now
	}
	return timestamps[0] + int64(s.window)
}

// expiresAt is when the newest recorded request ages out, leaving an
// empty log — the same as a key with no state. A log that is already
// empty has nothing worth keeping, so it expires right away.
func (s *SlidingWindowLog) expiresAt(timestamps []int64, now int64) int64 {
	if len(timestamps) == 0 {
		return now
	}
	return timestamps[len(timestamps)-1] + int64(s.window)
}
