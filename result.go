package ratelimit

import "time"

type Result struct {
	Allowed    bool
	Remaining  int
	RetryAfter time.Duration
	// ResetAt is in nanoseconds since the Unix epoch, on the Limiter's
	// Clock; time.Unix(0, r.ResetAt) turns it into a time.Time.
	ResetAt int64
	Limit   int
}
