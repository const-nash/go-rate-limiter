package ratelimit

import "time"

type Clock interface {
	Now() time.Time
}
