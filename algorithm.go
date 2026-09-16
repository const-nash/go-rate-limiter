package ratelimit

import "context"

// Algorithm decides whether a call identified by key is allowed, given
// the current time. Every key is independent: an implementation looks
// up and updates its own per-key state (typically via a KeyStore)
// rather than keeping a single shared counter for the whole instance.
//
// now is in nanoseconds since the Unix epoch, as read from the
// Limiter's Clock.
//
// ctx carries cancellation/timeouts through to a KeyStore that may
// do network I/O; err is non-nil only when the store itself failed
// (e.g. Redis unreachable), never for an ordinary "not allowed"
// decision — that's Result.Allowed = false with a nil error.
type Algorithm interface {
	Allow(ctx context.Context, now int64, key string) (Result, error)
	AllowN(ctx context.Context, now int64, key string, n int) (Result, error)
}
