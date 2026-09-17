package ratelimit_test

import (
	"context"
	"runtime"
	"slices"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	ratelimit "github.com/const-nash/go-rate-limiter"
)

// benchState stands in for an algorithm's per-key state: a counter and
// a timestamp, the shape both current algorithms have.
type benchState struct {
	count int
	last  int64
}

// bump is an UpdateFunc that keeps a key alive for a further hour.
func bump(now int64) ratelimit.UpdateFunc[benchState] {
	return func(prev benchState, _ bool) (benchState, int64, ratelimit.Result, error) {
		prev.count++
		prev.last = now
		return prev, now + int64(time.Hour), ratelimit.Result{Remaining: prev.count}, nil
	}
}

func storeKeys(n int) []string {
	keys := make([]string, n)
	for i := range keys {
		keys[i] = "10.0." + strconv.Itoa(i/256) + "." + strconv.Itoa(i%256)
	}
	return keys
}

// BenchmarkMapStore_UpdateSerial is the store's own cost, with no
// algorithm on top and nothing to contend with.
func BenchmarkMapStore_UpdateSerial(b *testing.B) {
	ctx := context.Background()
	s := ratelimit.NewMapStore[benchState]()
	now := int64(time.Hour)
	if _, err := s.Update(ctx, now, "warm", bump(now)); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	for b.Loop() {
		if _, err := s.Update(ctx, now, "warm", bump(now)); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkMapStore_UpdateParallel sweeps the shard count: it shows
// what WithShards buys, and that one shard is the old single-mutex
// store. Run it with -cpu 1,8.
func BenchmarkMapStore_UpdateParallel(b *testing.B) {
	for _, shards := range []int{1, 4, 16, 64, 256} {
		b.Run("shards="+strconv.Itoa(shards), func(b *testing.B) {
			keys := storeKeys(1024)
			s := ratelimit.NewMapStore[benchState](ratelimit.WithShards(shards))
			now := int64(time.Hour)

			ctx := context.Background()
			for _, key := range keys {
				if _, err := s.Update(ctx, now, key, bump(now)); err != nil {
					b.Fatal(err)
				}
			}

			b.ReportAllocs()
			b.ResetTimer()
			var goroutines atomic.Int64
			b.RunParallel(func(pb *testing.PB) {
				ctx := context.Background()
				i := int(goroutines.Add(1)) * 7919
				for pb.Next() {
					if _, err := s.Update(ctx, now, keys[i%len(keys)], bump(now)); err != nil {
						b.Error(err)
						return
					}
					i++
				}
			})
		})
	}
}

// BenchmarkMapStore_PausesDuringCleanup is about latency, not
// throughput: the janitor holds one shard's lock while it sweeps, so a
// call landing on that shard waits. It reports the worst and the 99th
// percentile call, which is what a caller would see as a spike; its
// ns/op average hides exactly that.
func BenchmarkMapStore_PausesDuringCleanup(b *testing.B) {
	const keys = 200_000
	ctx := context.Background()
	s := ratelimit.NewMapStore[benchState](ratelimit.WithCleanupInterval(time.Millisecond))
	b.Cleanup(func() {
		if err := s.Close(); err != nil {
			b.Errorf("unexpected error closing the store: %v", err)
		}
	})

	// Fill the store with keys that all expire while the benchmark runs,
	// so the janitor has real work to do on every shard.
	start := int64(time.Hour)
	names := storeKeys(keys)
	for _, name := range names {
		if _, err := s.Update(ctx, start, name, func(st benchState, _ bool) (benchState, int64, ratelimit.Result, error) {
			return st, start + 1, ratelimit.Result{}, nil
		}); err != nil {
			b.Fatal(err)
		}
	}

	now := start + int64(time.Minute)
	took := make([]time.Duration, 0, 1<<20)

	b.ResetTimer()
	for b.Loop() {
		call := time.Now()
		if _, err := s.Update(ctx, now, "live", bump(now)); err != nil {
			b.Fatal(err)
		}
		took = append(took, time.Since(call))
	}
	b.StopTimer()

	if len(took) == 0 {
		return
	}
	slices.Sort(took)
	b.ReportMetric(float64(took[len(took)*99/100]), "p99-ns")
	b.ReportMetric(float64(took[len(took)-1]), "max-ns")
}

// BenchmarkMapStore_MemoryPerKey reports what one more tracked key
// costs the store, key string included.
func BenchmarkMapStore_MemoryPerKey(b *testing.B) {
	ctx := context.Background()
	s := ratelimit.NewMapStore[benchState]()
	now := int64(time.Hour)

	before := heapInUse()
	keys := 0
	for b.Loop() {
		if _, err := s.Update(ctx, now, "key-"+strconv.Itoa(keys), bump(now)); err != nil {
			b.Fatal(err)
		}
		keys++
	}
	b.StopTimer()
	after := heapInUse()
	// Keep the store reachable across the measurement, or it is
	// collected before it can be measured.
	runtime.KeepAlive(s)
	b.ReportMetric(float64(after-before)/float64(keys), "B/key")
	b.ReportMetric(0, "ns/op")
}

// BenchmarkSystemClock_Now guards the clock against becoming a cost
// worth noticing: it is read once per Limiter call.
func BenchmarkSystemClock_Now(b *testing.B) {
	c := ratelimit.SystemClock{}
	var sink int64
	for b.Loop() {
		sink = c.Now()
	}
	if sink == 0 {
		b.Fatal("clock returned zero")
	}
}
