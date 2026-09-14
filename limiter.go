package ratelimit

import "context"

type Limiter struct {
	algorithm Algorithm
	clock     Clock
}

func New(algorithm Algorithm, clock Clock) *Limiter {
	return &Limiter{
		algorithm: algorithm,
		clock:     clock,
	}
}

func (l *Limiter) Allow(ctx context.Context, key string) (Result, error) {
	return l.algorithm.Allow(ctx, l.clock.Now(), key)
}

func (l *Limiter) AllowN(ctx context.Context, key string, n int) (Result, error) {
	return l.algorithm.AllowN(ctx, l.clock.Now(), key, n)
}
