package ratelimit

import "time"

type Algorithm interface {
	Allow(now time.Time) bool
	AllowN(now time.Time, n int) bool
}
