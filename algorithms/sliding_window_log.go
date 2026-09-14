package algorithms

import (
	"context"
	"sync"
	"time"

	ratelimit "github.com/const-nash/go-rate-limiter"
	"github.com/const-nash/go-rate-limiter/internal/validate"
)

// SlidingWindowLog allows up to limit requests within any trailing
// window of the given duration, tracked independently per key. Unlike
// FixedWindowCounter, it keeps a timestamp per accepted request
// instead of a single counter, and on every call discards whichever
// timestamps have aged out of [now-window, now) before counting the
// rest. That gives an exact sliding window — no boundary burst — at
// the cost of O(limit) memory per key instead of FixedWindowCounter's
// O(1).
type SlidingWindowLog struct {
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

func NewSlidingWindowLog(limit int, window time.Duration, store ratelimit.KeyStore) *SlidingWindowLog {
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
	s.mu.Lock()
	defer s.mu.Unlock()

	loaded, err := s.loadTimestamps(ctx, key)
	if err != nil {
		return ratelimit.Result{}, err
	}
	timestamps := s.evict(loaded, now)

	if len(timestamps)+n > s.limit {
		if err := s.store.Set(ctx, key, timestamps); err != nil {
			return ratelimit.Result{}, err
		}
		return ratelimit.Result{
			Allowed:    false,
			Remaining:  s.limit - len(timestamps),
			RetryAfter: s.resetAt(timestamps, now).Sub(now),
			ResetAt:    s.resetAt(timestamps, now),
			Limit:      s.limit,
		}, nil
	}

	for i := 0; i < n; i++ {
		timestamps = append(timestamps, now)
	}
	if err := s.store.Set(ctx, key, timestamps); err != nil {
		return ratelimit.Result{}, err
	}

	return ratelimit.Result{
		Allowed:   true,
		Remaining: s.limit - len(timestamps),
		ResetAt:   s.resetAt(timestamps, now),
		Limit:     s.limit,
	}, nil
}

func (s *SlidingWindowLog) loadTimestamps(ctx context.Context, key string) ([]time.Time, error) {
	raw, ok, err := s.store.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	timestamps, ok := raw.([]time.Time)
	if !ok {
		return nil, nil
	}
	return timestamps, nil
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
