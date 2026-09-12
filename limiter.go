package ratelimit

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

func (l *Limiter) Allow() bool {
	return l.algorithm.Allow(l.clock.Now())
}

func (l *Limiter) AllowN(n int) bool {
	return l.algorithm.AllowN(l.clock.Now(), n)
}
