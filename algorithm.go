package ratelimit

import "time"

type Algorithm interface {
	Allow(now time.Time) Result
	AllowN(now time.Time, n int) Result
}
