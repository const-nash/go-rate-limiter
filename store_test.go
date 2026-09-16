package ratelimit_test

import (
	"context"
	"errors"
	"runtime"
	"strconv"
	"testing"
	"time"

	ratelimit "github.com/const-nash/go-rate-limiter"
)

type counterState struct {
	n int
}

// set returns an UpdateFunc that stores n with the given expiry and
// reports what it was handed, for the test to check.
func set(n int, expiresAt int64, gotPrev *counterState, gotOK *bool) ratelimit.UpdateFunc[counterState] {
	return func(prev counterState, ok bool) (counterState, int64, ratelimit.Result, error) {
		if gotPrev != nil {
			*gotPrev = prev
		}
		if gotOK != nil {
			*gotOK = ok
		}
		return counterState{n: n}, expiresAt, ratelimit.Result{Remaining: n}, nil
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

// waitFor polls cond until it holds, failing the test with msg if it
// doesn't within a few seconds.
func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal(msg)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestMapStore_UpdateStoresState(t *testing.T) {
	ctx := context.Background()
	s := ratelimit.NewMapStore[counterState]()

	var prev counterState
	var ok bool
	res, err := s.Update(ctx, 100, "k", set(1, 200, &prev, &ok))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok || prev != (counterState{}) {
		t.Fatalf("first update: expected absent zero state, got ok=%v prev=%+v", ok, prev)
	}
	if res.Remaining != 1 {
		t.Fatalf("expected the UpdateFunc's Result to be returned, got %+v", res)
	}

	if _, err := s.Update(ctx, 150, "k", set(2, 200, &prev, &ok)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok || prev.n != 1 {
		t.Fatalf("second update: expected stored state n=1, got ok=%v prev=%+v", ok, prev)
	}
}

func TestMapStore_ExpiredEntryReadsAsAbsent(t *testing.T) {
	ctx := context.Background()
	s := ratelimit.NewMapStore[counterState]()

	if _, err := s.Update(ctx, 100, "k", set(1, 200, nil, nil)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var prev counterState
	var ok bool
	if _, err := s.Update(ctx, 200, "k", set(2, 300, &prev, &ok)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok || prev != (counterState{}) {
		t.Fatalf("expected an entry at its expiry to read as absent, got ok=%v prev=%+v", ok, prev)
	}
}

func TestMapStore_UpdateErrorWritesNothing(t *testing.T) {
	ctx := context.Background()
	s := ratelimit.NewMapStore[counterState]()
	wantErr := errors.New("boom")

	if _, err := s.Update(ctx, 100, "k", set(1, 200, nil, nil)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	res, err := s.Update(ctx, 110, "k", func(counterState, bool) (counterState, int64, ratelimit.Result, error) {
		return counterState{n: 99}, 300, ratelimit.Result{Remaining: 99}, wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected the UpdateFunc's error, got %v", err)
	}
	if res != (ratelimit.Result{}) {
		t.Fatalf("expected a zero Result alongside the error, got %+v", res)
	}

	var prev counterState
	if _, err := s.Update(ctx, 120, "k", set(1, 200, &prev, nil)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if prev.n != 1 {
		t.Fatalf("expected the failed update to leave n=1, got %+v", prev)
	}
}

func TestMapStore_Delete(t *testing.T) {
	ctx := context.Background()
	s := ratelimit.NewMapStore[counterState]()

	if _, err := s.Update(ctx, 100, "k", set(1, 200, nil, nil)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := s.Delete(ctx, "k"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var ok bool
	if _, err := s.Update(ctx, 110, "k", set(1, 200, nil, &ok)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("expected a deleted key to read as absent")
	}
}

func TestMapStore_CleanupReclaimsMemoryOfExpiredKeys(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates a large burst of keys")
	}
	ctx := context.Background()

	const keys = 200_000
	names := make([]string, keys)
	for i := range names {
		names[i] = "10.0." + strconv.Itoa(i)
	}

	base := heapInUse()
	s := ratelimit.NewMapStore[counterState](ratelimit.WithCleanupInterval(time.Millisecond))
	defer s.Close()

	// A burst of one-shot keys, all expiring a moment later.
	start := int64(time.Hour)
	for _, name := range names {
		if _, err := s.Update(ctx, start, name, set(1, start+1, nil, nil)); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	peak := heapInUse() - base
	if peak < 4<<20 {
		t.Fatalf("burst only took %d bytes; too small to tell reclaimed memory from noise", peak)
	}

	// Traffic moves on, which is how the store learns time has passed.
	later := start + int64(time.Minute)
	if _, err := s.Update(ctx, later, "live", set(1, later+int64(time.Hour), nil, nil)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var retained int64
	waitFor(t, func() bool {
		retained = heapInUse() - base
		return retained < peak/4
	}, "expected the janitor to give back the memory of expired keys")

	// The live key survived the sweep.
	var ok bool
	if _, err := s.Update(ctx, later, "live", set(1, later+int64(time.Hour), nil, &ok)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected the live key to be kept")
	}
	runtime.KeepAlive(names)
}

func TestMapStore_CloseStopsJanitor(t *testing.T) {
	ctx := context.Background()
	before := runtime.NumGoroutine()
	s := ratelimit.NewMapStore[counterState](ratelimit.WithCleanupInterval(time.Millisecond))

	if err := s.Close(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	waitFor(t, func() bool { return runtime.NumGoroutine() <= before },
		"expected the janitor goroutine to exit after Close")
	if err := s.Close(); err != nil {
		t.Fatalf("expected a second Close to be a no-op, got %v", err)
	}

	// The store stays usable after Close.
	var prev counterState
	if _, err := s.Update(ctx, 100, "k", set(1, 200, nil, nil)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := s.Update(ctx, 110, "k", set(2, 200, &prev, nil)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if prev.n != 1 {
		t.Fatalf("expected the store to keep working after Close, got %+v", prev)
	}
}

func TestMapStore_CloseWithoutJanitor(t *testing.T) {
	s := ratelimit.NewMapStore[counterState]()
	if err := s.Close(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWithCleanupInterval_InvalidPanics(t *testing.T) {
	for _, interval := range []time.Duration{0, -time.Second} {
		t.Run(interval.String(), func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("expected panic for non-positive cleanup interval")
				}
			}()
			ratelimit.WithCleanupInterval(interval)
		})
	}
}
