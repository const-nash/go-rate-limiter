package algorithms_test

import (
	"context"
	"strconv"
	"testing"
	"time"

	ratelimit "github.com/const-nash/go-rate-limiter"
	"github.com/const-nash/go-rate-limiter/algorithms"
)

// Scenarios peculiar to FixedWindowCounter. What every algorithm is
// measured on lives in bench_test.go.

func newFixedWindow(limit int, window time.Duration) *algorithms.FixedWindowCounter {
	return algorithms.NewFixedWindowCounter(limit, window,
		ratelimit.NewMapStore[algorithms.FixedWindowState]())
}

// BenchmarkFixedWindowCounter_WindowRollover walks time forward a whole
// window per call, so advance resets the counter every time: the
// branch a long-lived key takes once per window.
func BenchmarkFixedWindowCounter_WindowRollover(b *testing.B) {
	const window = time.Minute
	ctx := context.Background()
	fw := newFixedWindow(1<<30, window)
	now := at(0)

	b.ReportAllocs()
	for b.Loop() {
		now += int64(window)
		if _, err := fw.Allow(ctx, now, "rolling"); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkFixedWindowCounter_FirstCallPerKey is the cost of admitting
// a key never seen before — the path a burst of fresh keys takes, and
// the one that grows the store.
func BenchmarkFixedWindowCounter_FirstCallPerKey(b *testing.B) {
	ctx := context.Background()
	fw := newFixedWindow(1<<30, time.Hour)
	now := at(0)

	b.ReportAllocs()
	i := 0
	for b.Loop() {
		if _, err := fw.Allow(ctx, now, "fresh-"+strconv.Itoa(i)); err != nil {
			b.Fatal(err)
		}
		i++
	}
}

// BenchmarkFixedWindowCounter_AllowN checks that admitting n at once
// costs the same as admitting one: the counter moves by n either way.
func BenchmarkFixedWindowCounter_AllowN(b *testing.B) {
	for _, n := range []int{1, 8, 64} {
		b.Run("n="+strconv.Itoa(n), func(b *testing.B) {
			ctx := context.Background()
			fw := newFixedWindow(1<<30, time.Hour)
			now := at(0)

			b.ReportAllocs()
			for b.Loop() {
				if _, err := fw.AllowN(ctx, now, "bulk", n); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
