package ratelimit

import (
	"context"
	"hash/maphash"
	"math/bits"
	"sync"
)

// UpdateFunc computes a key's next state, and the decision that goes
// with it, from the key's current state.
//
// prev is the key's stored state, and ok reports whether the key had
// any state at all — a distinction that matters to algorithms whose
// "no state yet" differs from their zero value (a token bucket starts
// a new key at full burst, not at zero tokens). When ok is false, prev
// is the zero value of T.
//
// out is handed straight back to the caller by Update. It is returned
// rather than captured in a closure variable so that it stays on the
// stack: a captured Result escapes to the heap on every single call.
//
// Returning an error aborts the update: nothing is written, and the
// error is returned from Update unchanged alongside a zero Result.
//
// A store runs fn while holding whatever lock or transaction guards
// the key, so fn must not block: no I/O, no calls back into the
// store, no waiting on another goroutine. A store backed by a network
// service may also retry a conflicting update, calling fn more than
// once for a single Update; fn must therefore derive its result only
// from its arguments and the enclosing algorithm's configuration,
// never from a counter it increments on the side.
type UpdateFunc[T any] func(prev T, ok bool) (next T, out Result, err error)

// KeyStore stores per-key algorithm state of type T under a
// caller-chosen string key (a user ID, an API key, an IP address,
// ...). A concrete Algorithm uses it to load and persist the state
// for one key without knowing how or where it's stored, so the
// storage strategy (in memory, Redis, ...) can be swapped without
// touching any algorithm's rate-limiting math.
//
// T is the algorithm's own state type, so the store is typed rather
// than holding an any. That keeps each stored state on the map's
// value line instead of boxing it into an interface on every write,
// and it makes handing one algorithm's store to another algorithm a
// compile error rather than a silent quota reset at run time.
//
// Read-modify-write is a single operation, Update, rather than a Get
// followed by a Set. An algorithm has to read a key's state, decide,
// and write the result atomically, and only the store knows how to
// make that atomic for its own backend: MapStore takes one shard
// lock, while a Redis-backed store would use a script or an
// optimistic retry. Exposing Get and Set separately would leave the
// gap between them for the caller to guard, which in practice means
// one mutex per algorithm instance serializing every key — correct,
// but a hard ceiling on throughput, and across two processes not even
// correct.
//
// Update returns a Result, which the store itself never inspects — it
// only carries back whatever the UpdateFunc decided. Threading it
// through the store rather than through a captured variable is what
// keeps an algorithm's Result on the stack; Go cannot parameterize an
// interface method on a second type, so the alternative to naming
// Result here would be a KeyStore[T, R] that every call site has to
// spell out twice.
//
// Every method takes a context.Context and returns an error, since an
// implementation backed by a network store (Redis, ...) can block on
// or fail the round trip; callers should propagate ctx for
// cancellation/timeouts and must not treat a non-nil err as "key not
// found" — that is reported to an UpdateFunc via its ok argument.
type KeyStore[T any] interface {
	Update(ctx context.Context, key string, fn UpdateFunc[T]) (Result, error)
	Delete(ctx context.Context, key string) error
}

// defaultShards is the number of independently locked shards a
// MapStore is built with. It is a power of two so a key's hash maps
// to a shard with a mask rather than a division, and is comfortably
// above GOMAXPROCS on ordinary hardware so that concurrent callers
// rarely land on the same shard.
const defaultShards = 64

// shardPad is what's left of a 128-byte cache line (the line size on
// arm64; x86-64's 64 divides it evenly) after a mutex and a map header.
const shardPad = 128 - 16

// mapShard is one independently locked slice of a MapStore's keys.
type mapShard[T any] struct {
	mu    sync.Mutex
	items map[string]T
	// pad keeps each shard's mutex on its own cache line, so that a
	// goroutine taking one shard's lock doesn't invalidate the cache
	// line holding a neighbouring shard's lock (false sharing).
	_ [shardPad]byte
}

// MapStore is a KeyStore backed by in-memory maps, safe for
// concurrent use. It is the store to reach for by default; a
// distributed backend (Redis, ...) can implement the same interface
// without changing any algorithm. Its operations never block on I/O,
// so it ignores ctx and never returns a non-nil error of its own — it
// returns only what an UpdateFunc returns.
//
// Keys are spread across independently locked shards, so calls for
// different keys usually proceed in parallel and only calls colliding
// on one shard wait for each other. There is deliberately no lock
// covering the store as a whole.
type MapStore[T any] struct {
	seed   maphash.Seed
	shards []mapShard[T]
}

// NewMapStore returns an empty MapStore holding state of type T, with
// the default number of shards. T is the state type of whichever
// algorithm the store is for, so the call names it explicitly:
//
//	store := ratelimit.NewMapStore[algorithms.FixedWindowState]()
//	fw := algorithms.NewFixedWindowCounter(100, time.Minute, store)
//
// Note that it never evicts: a key, once seen, keeps its entry until
// it is explicitly deleted. A long-lived process limiting on
// unbounded keys (per-IP, say) needs an eviction policy on top.
func NewMapStore[T any]() *MapStore[T] {
	return newMapStore[T](defaultShards)
}

// newMapStore builds a MapStore with n shards, rounded up to a power
// of two. It exists so tests and benchmarks can vary the shard count;
// callers get the default via NewMapStore.
func newMapStore[T any](n int) *MapStore[T] {
	if n < 1 {
		n = 1
	}
	if n&(n-1) != 0 {
		n = 1 << bits.Len(uint(n))
	}

	s := &MapStore[T]{
		seed:   maphash.MakeSeed(),
		shards: make([]mapShard[T], n),
	}
	for i := range s.shards {
		s.shards[i].items = make(map[string]T)
	}
	return s
}

// shard picks the shard owning key. len(s.shards) is a power of two,
// so the mask is equivalent to a modulo.
func (s *MapStore[T]) shard(key string) *mapShard[T] {
	return &s.shards[maphash.String(s.seed, key)&uint64(len(s.shards)-1)]
}

func (s *MapStore[T]) Update(_ context.Context, key string, fn UpdateFunc[T]) (Result, error) {
	sh := s.shard(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	prev, ok := sh.items[key]
	next, out, err := fn(prev, ok)
	if err != nil {
		return Result{}, err
	}
	sh.items[key] = next
	return out, nil
}

func (s *MapStore[T]) Delete(_ context.Context, key string) error {
	sh := s.shard(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	delete(sh.items, key)
	return nil
}
