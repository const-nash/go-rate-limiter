package algorithms_test

import (
	"context"

	ratelimit "github.com/const-nash/go-rate-limiter"
)

// expirySpy is a KeyStore that forwards to another one and records the
// expiresAt of the most recent update, so tests can check what an
// algorithm promises the store about when its state stops mattering.
type expirySpy[T any] struct {
	ratelimit.KeyStore[T]
	lastExpiresAt int64
}

func newExpirySpy[T any]() *expirySpy[T] {
	return &expirySpy[T]{KeyStore: ratelimit.NewMapStore[T]()}
}

func (s *expirySpy[T]) Update(ctx context.Context, now int64, key string, fn ratelimit.UpdateFunc[T]) (ratelimit.Result, error) {
	return s.KeyStore.Update(ctx, now, key, func(prev T, ok bool) (T, int64, ratelimit.Result, error) {
		next, expiresAt, out, err := fn(prev, ok)
		s.lastExpiresAt = expiresAt
		return next, expiresAt, out, err
	})
}
