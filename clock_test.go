package ratelimit_test

import (
	"testing"
	"time"

	ratelimit "github.com/const-nash/go-rate-limiter"
)

func TestSystemClock_TracksWallClock(t *testing.T) {
	got := ratelimit.SystemClock{}.Now()
	wall := time.Now().UnixNano()

	// It only departs from the wall clock by steps and drift since the
	// package was initialized, which is moments ago in a test binary.
	if diff := time.Duration(wall - got).Abs(); diff > time.Second {
		t.Fatalf("expected SystemClock within 1s of the wall clock, off by %v", diff)
	}
}

func TestSystemClock_NeverGoesBackwards(t *testing.T) {
	c := ratelimit.SystemClock{}
	prev := c.Now()
	for i := 0; i < 10000; i++ {
		now := c.Now()
		if now < prev {
			t.Fatalf("clock went backwards: %d after %d", now, prev)
		}
		prev = now
	}
}
