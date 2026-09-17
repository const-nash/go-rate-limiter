package algorithms_test

import (
	"context"
	"runtime"
	"strconv"
	"testing"
	"time"

	ratelimit "github.com/const-nash/go-rate-limiter"
	"github.com/const-nash/go-rate-limiter/algorithms"
)

// Scenarios peculiar to SlidingWindowLog. What every algorithm is
// measured on lives in bench_test.go.

func newSlidingWindow(limit int, window time.Duration) *algorithms.SlidingWindowLog {
	return algorithms.NewSlidingWindowLog(limit, window,
		ratelimit.NewMapStore[algorithms.SlidingWindowState]())
}

// slidingLimits are the log lengths the benchmarks sweep. The
// algorithm is O(limit) in memory per key and evicts by scanning, so
// the cost of a call depends on this and on nothing else in the same
// way the other algorithm's does.
var slidingLimits = []int{10, 100, 1000}

// BenchmarkSlidingWindowLog_SteadyState keeps the window exactly full
// and walks time forward one slot per call, so every iteration both
// evicts the oldest timestamp and appends a new one. This is the
// scenario a busy key actually sits in — unlike a benchmark on a fixed
// now, which fills the log once and then only measures denials.
func BenchmarkSlidingWindowLog_SteadyState(b *testing.B) {
	const window = time.Second
	for _, limit := range slidingLimits {
		b.Run("limit="+strconv.Itoa(limit), func(b *testing.B) {
			ctx := context.Background()
			sw := newSlidingWindow(limit, window)
			step := int64(window) / int64(limit)
			now := at(0)

			// Fill the window before the timer starts.
			for i := 0; i < limit; i++ {
				now += step
				if _, err := sw.Allow(ctx, now, "hot"); err != nil {
					b.Fatal(err)
				}
			}

			b.ReportAllocs()
			for b.Loop() {
				now += step
				res, err := sw.Allow(ctx, now, "hot")
				if err != nil {
					b.Fatal(err)
				}
				if !res.Allowed {
					b.Fatal("expected a freed slot in a full sliding window")
				}
			}
		})
	}
}

// BenchmarkSlidingWindowLog_EvictWholeLog jumps a full window ahead
// every call, so each one discards the entire log at once: the worst
// case for the eviction scan.
func BenchmarkSlidingWindowLog_EvictWholeLog(b *testing.B) {
	const window = time.Second
	for _, limit := range slidingLimits {
		b.Run("limit="+strconv.Itoa(limit), func(b *testing.B) {
			ctx := context.Background()
			sw := newSlidingWindow(limit, window)
			now := at(0)

			b.ReportAllocs()
			for b.Loop() {
				if _, err := sw.AllowN(ctx, now, "bursty", limit); err != nil {
					b.Fatal(err)
				}
				now += int64(window) + 1
			}
		})
	}
}

// BenchmarkSlidingWindowLog_MemoryPerKey shows the O(limit) memory the
// log costs per tracked key, which is the price it pays over a fixed
// window's O(1). The figure includes the key string itself.
func BenchmarkSlidingWindowLog_MemoryPerKey(b *testing.B) {
	for _, limit := range slidingLimits {
		b.Run("limit="+strconv.Itoa(limit), func(b *testing.B) {
			ctx := context.Background()
			sw := newSlidingWindow(limit, time.Hour)
			now := at(0)

			before := heapInUse()
			keys := 0
			for b.Loop() {
				if _, err := sw.AllowN(ctx, now, "key-"+strconv.Itoa(keys), limit); err != nil {
					b.Fatal(err)
				}
				keys++
			}
			b.StopTimer()
			after := heapInUse()
			// Keep the log reachable across the measurement, or it is
			// collected before it can be measured.
			runtime.KeepAlive(sw)
			b.ReportMetric(float64(after-before)/float64(keys), "B/key")
			b.ReportMetric(0, "ns/op")
		})
	}
}
