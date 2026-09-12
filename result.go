package ratelimit

import "time"

type Result struct {
	Allowed    bool
	Remaining  int
	RetryAfter time.Duration
	ResetAt    time.Time
	Limit      int
}
