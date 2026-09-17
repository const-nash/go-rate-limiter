package algorithms_test

import (
	"context"
	"runtime"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	ratelimit "github.com/const-nash/go-rate-limiter"
	"github.com/const-nash/go-rate-limiter/algorithms"
)

// This file holds the benchmarks that every algorithm runs, so their
// numbers are comparable; scenarios peculiar to one algorithm live in
// its own <algorithm>_bench_test.go.

// algorithmCase is one Algorithm implementation under benchmark,
// together with how to build it. Each algorithm owns its state type,
// so building the store is part of the case rather than shared.
type algorithmCase struct {
	name  string
	build func(limit int, window time.Duration) ratelimit.Algorithm
	// buildSlow backs the algorithm with a store that costs latency per
	// operation, standing in for a network-backed KeyStore.
	buildSlow func(limit int, window time.Duration, latency time.Duration) ratelimit.Algorithm
}

var algorithmCases = []algorithmCase{
	{
		name: "FixedWindowCounter",
		build: func(limit int, window time.Duration) ratelimit.Algorithm {
			return algorithms.NewFixedWindowCounter(limit, window,
				ratelimit.NewMapStore[algorithms.FixedWindowState]())
		},
		buildSlow: func(limit int, window time.Duration, latency time.Duration) ratelimit.Algorithm {
			return algorithms.NewFixedWindowCounter(limit, window,
				newLatentStore[algorithms.FixedWindowState](latency))
		},
	},
	{
		name: "SlidingWindowLog",
		build: func(limit int, window time.Duration) ratelimit.Algorithm {
			return algorithms.NewSlidingWindowLog(limit, window,
				ratelimit.NewMapStore[algorithms.SlidingWindowState]())
		},
		buildSlow: func(limit int, window time.Duration, latency time.Duration) ratelimit.Algorithm {
			return algorithms.NewSlidingWindowLog(limit, window,
				newLatentStore[algorithms.SlidingWindowState](latency))
		},
	},
}

// benchKeys returns n distinct keys, shaped like the identifiers a
// caller limits on.
func benchKeys(n int) []string {
	keys := make([]string, n)
	for i := range keys {
		keys[i] = "10.0." + strconv.Itoa(i/256) + "." + strconv.Itoa(i%256)
	}
	return keys
}

// latentStore wraps a MapStore with a fixed cost per operation, the way
// a store reached over the network would. It exists to catch a
// regression that once capped the whole limiter at ~800 req/s: holding
// a lock across the round trip.
type latentStore[T any] struct {
	inner   *ratelimit.MapStore[T]
	latency time.Duration
}

func newLatentStore[T any](latency time.Duration) *latentStore[T] {
	return &latentStore[T]{inner: ratelimit.NewMapStore[T](), latency: latency}
}

func (s *latentStore[T]) Update(ctx context.Context, now int64, key string, fn ratelimit.UpdateFunc[T]) (ratelimit.Result, error) {
	time.Sleep(s.latency)
	return s.inner.Update(ctx, now, key, fn)
}

func (s *latentStore[T]) Delete(ctx context.Context, key string) error {
	time.Sleep(s.latency)
	return s.inner.Delete(ctx, key)
}

// runParallel runs body on every parallel goroutine, handing each its
// own starting offset into the key set so that they don't share a
// counter — a shared one would measure atomic contention instead of
// the limiter.
func runParallel(b *testing.B, keys []string, algorithm ratelimit.Algorithm, now int64) {
	b.Helper()
	var goroutines atomic.Int64
	b.RunParallel(func(pb *testing.PB) {
		ctx := context.Background()
		i := int(goroutines.Add(1)) * 7919 // a prime, to spread starting points
		for pb.Next() {
			if _, err := algorithm.AllowN(ctx, now, keys[i%len(keys)], 1); err != nil {
				b.Error(err)
				return
			}
			i++
		}
	})
}

// BenchmarkAlgorithms_SerialOneKey is the cost of one uncontended
// call: the number to watch for allocations.
func BenchmarkAlgorithms_SerialOneKey(b *testing.B) {
	for _, c := range algorithmCases {
		b.Run(c.name, func(b *testing.B) {
			ctx := context.Background()
			// A limit high enough that no iteration is ever denied.
			algorithm := c.build(1<<30, time.Hour)
			now := at(0)
			if _, err := algorithm.Allow(ctx, now, "warm"); err != nil {
				b.Fatal(err)
			}

			b.ReportAllocs()
			for b.Loop() {
				if _, err := algorithm.Allow(ctx, now, "warm"); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkAlgorithms_ParallelManyKeys is how the limiter scales with
// cores when keys are spread out: run it with -cpu 1,8. Before
// atomicity moved into the store, this got slower as cores were added.
func BenchmarkAlgorithms_ParallelManyKeys(b *testing.B) {
	for _, c := range algorithmCases {
		b.Run(c.name, func(b *testing.B) {
			keys := benchKeys(1024)
			algorithm := c.build(1<<30, time.Hour)
			now := at(0)

			ctx := context.Background()
			for _, key := range keys {
				if _, err := algorithm.Allow(ctx, now, key); err != nil {
					b.Fatal(err)
				}
			}

			b.ReportAllocs()
			b.ResetTimer()
			runParallel(b, keys, algorithm, now)
		})
	}
}

// BenchmarkAlgorithms_ParallelHotKey is the floor sharding cannot
// raise: every call for one key serializes on that key's shard.
func BenchmarkAlgorithms_ParallelHotKey(b *testing.B) {
	for _, c := range algorithmCases {
		b.Run(c.name, func(b *testing.B) {
			algorithm := c.build(1<<30, time.Hour)
			now := at(0)
			if _, err := algorithm.Allow(context.Background(), now, "hot"); err != nil {
				b.Fatal(err)
			}

			b.ReportAllocs()
			b.ResetTimer()
			runParallel(b, []string{"hot"}, algorithm, now)
		})
	}
}

// BenchmarkAlgorithms_Denied measures the other branch: the quota is
// already spent, so no state is appended and the answer is no.
func BenchmarkAlgorithms_Denied(b *testing.B) {
	for _, c := range algorithmCases {
		b.Run(c.name, func(b *testing.B) {
			const limit = 100
			ctx := context.Background()
			algorithm := c.build(limit, time.Hour)
			now := at(0)
			for i := 0; i < limit; i++ {
				if _, err := algorithm.Allow(ctx, now, "spent"); err != nil {
					b.Fatal(err)
				}
			}

			b.ReportAllocs()
			for b.Loop() {
				res, err := algorithm.Allow(ctx, now, "spent")
				if err != nil {
					b.Fatal(err)
				}
				if res.Allowed {
					b.Fatal("expected the quota to be spent")
				}
			}
		})
	}
}

// BenchmarkAlgorithms_SlowStore reports throughput against a store
// that costs a round trip, which is the shape a Redis-backed store
// has. Being built on time.Sleep it is far noisier than the others:
// read it for orders of magnitude, not percentages.
func BenchmarkAlgorithms_SlowStore(b *testing.B) {
	const latency = 500 * time.Microsecond
	for _, c := range algorithmCases {
		b.Run(c.name, func(b *testing.B) {
			keys := benchKeys(256)
			algorithm := c.buildSlow(1<<30, time.Hour, latency)
			now := at(0)

			b.ResetTimer()
			start := time.Now()
			runParallel(b, keys, algorithm, now)
			b.StopTimer()
			b.ReportMetric(float64(b.N)/time.Since(start).Seconds(), "req/s")
		})
	}
}

// BenchmarkAlgorithms_MemoryPerKey reports how much memory one more
// tracked key costs, which is what decides whether a per-IP limiter
// fits in RAM. The figure includes the key string itself.
func BenchmarkAlgorithms_MemoryPerKey(b *testing.B) {
	for _, c := range algorithmCases {
		b.Run(c.name, func(b *testing.B) {
			const limit = 100
			ctx := context.Background()
			algorithm := c.build(limit, time.Hour)
			now := at(0)

			before := heapInUse()
			keys := 0
			for b.Loop() {
				// Each key gets its quota filled, so the state is as
				// large as the algorithm ever makes it.
				key := "key-" + strconv.Itoa(keys)
				if _, err := algorithm.AllowN(ctx, now, key, limit); err != nil {
					b.Fatal(err)
				}
				keys++
			}
			b.StopTimer()
			after := heapInUse()
			// Without this the algorithm, and with it every key it
			// tracks, is unreachable by the time heapInUse collects —
			// so the measurement would come out at roughly zero.
			runtime.KeepAlive(algorithm)
			b.ReportMetric(float64(after-before)/float64(keys), "B/key")
			// The loop is dominated by measurement, so its ns/op would
			// only be noise next to the other benchmarks.
			b.ReportMetric(0, "ns/op")
		})
	}
}

// heapInUse returns the live heap size after a full collection.
func heapInUse() int64 {
	runtime.GC()
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return int64(m.HeapAlloc)
}
