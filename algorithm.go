package ratelimit

import (
	"context"
	"time"
)

// Algorithm decides whether a call identified by key is allowed, given
// the current time. Every key is independent: an implementation looks
// up and updates its own per-key state (typically via a KeyStore)
// rather than keeping a single shared counter for the whole instance.
//
// ctx carries cancellation/timeouts through to a KeyStore that may
// do network I/O; err is non-nil only when the store itself failed
// (e.g. Redis unreachable), never for an ordinary "not allowed"
// decision — that's Result.Allowed = false with a nil error.
type Algorithm interface {
	Allow(ctx context.Context, now time.Time, key string) (Result, error)
	AllowN(ctx context.Context, now time.Time, key string, n int) (Result, error)
}
