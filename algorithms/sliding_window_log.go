package algorithms

import (
	"sync"
	"time"

	ratelimit "github.com/const-nash/go-rate-limiter"
	"github.com/const-nash/go-rate-limiter/internal/validate"
)

// SlidingWindowLog allows up to limit requests within any trailing
// window of the given duration. Unlike FixedWindowCounter, it keeps
// a timestamp per accepted request instead of a single counter, and
// on every call discards whichever timestamps have aged out of
// [now-window, now) before counting the rest. That gives an exact
// sliding window — no boundary burst — at the cost of O(limit)
// memory instead of FixedWindowCounter's O(1).
type SlidingWindowLog struct {
	limit      int
	window     time.Duration
	timestamps []time.Time
	mu         sync.Mutex
}

func NewSlidingWindowLog(limit int, window time.Duration) *SlidingWindowLog {
	if err := validate.Limit(limit); err != nil {
		panic(err)
	}
	if err := validate.Window(window); err != nil {
		panic(err)
	}

	return &SlidingWindowLog{
		limit:  limit,
		window: window,
	}
}

func (s *SlidingWindowLog) Allow(now time.Time) ratelimit.Result {
	return s.AllowN(now, 1)
}

func (s *SlidingWindowLog) AllowN(now time.Time, n int) ratelimit.Result {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.evict(now)

	if len(s.timestamps)+n > s.limit {
		return ratelimit.Result{
			Allowed:    false,
			Remaining:  s.limit - len(s.timestamps),
			RetryAfter: s.resetAt(now).Sub(now),
			ResetAt:    s.resetAt(now),
			Limit:      s.limit,
		}
	}

	for i := 0; i < n; i++ {
		s.timestamps = append(s.timestamps, now)
	}

	return ratelimit.Result{
		Allowed:   true,
		Remaining: s.limit - len(s.timestamps),
		ResetAt:   s.resetAt(now),
		Limit:     s.limit,
	}
}

// evict drops every timestamp that has aged out of the trailing
// window. Timestamps are always appended in non-decreasing order, so
// the survivors are a suffix and a single forward scan finds them.
func (s *SlidingWindowLog) evict(now time.Time) {
	threshold := now.Add(-s.window)
	i := 0
	for i < len(s.timestamps) && !s.timestamps[i].After(threshold) {
		i++
	}
	s.timestamps = s.timestamps[i:]
}

// resetAt is when the oldest recorded request ages out of the
// window, freeing the next slot. With no recorded requests the
// window already has room, so it resolves to now.
func (s *SlidingWindowLog) resetAt(now time.Time) time.Time {
	if len(s.timestamps) == 0 {
		return now
	}
	return s.timestamps[0].Add(s.window)
}
