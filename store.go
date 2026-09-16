package ratelimit

import (
	"context"
	"hash/maphash"
	"sync"
	"time"

	"github.com/const-nash/go-rate-limiter/internal/validate"
)

// UpdateFunc computes a key's next state, when that state expires, and
// the decision that goes with it, all from the key's current state.
//
// prev is the key's stored state, and ok reports whether the key had
// any live state at all — a distinction that matters to algorithms
// whose "no state yet" differs from their zero value (a token bucket
// starts a new key at full burst, not at zero tokens). When ok is
// false, prev is the zero value of T. An entry that has expired is
// reported as absent, never handed back.
//
// expiresAt is the instant, on the same clock as the now passed to
// Update, from which next is indistinguishable from no state at all:
// every decision the algorithm would make from next at or after
// expiresAt, it would also make from an absent key. A store relies on
// exactly that when it drops the key, so an algorithm must not return
// an earlier expiresAt than it can vouch for; one that can't tell
// returns math.MaxInt64. An expiresAt at or before now means next is
// already as good as absent, and the store need not keep it at all.
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
type UpdateFunc[T any] func(prev T, ok bool) (next T, expiresAt int64, out Result, err error)

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
// Expiry is split the same way. Only the algorithm knows when a key's
// state stops mattering, so it reports that as the UpdateFunc's
// expiresAt; only the store knows how to forget a key, so it does
// that — MapStore with a background sweep, a Redis-backed store with
// PEXPIREAT in the same script as the update. now is the caller's
// current time, which Update compares expiresAt against; a store
// never consults a clock of its own for that, so it can't disagree
// with the Clock the Limiter reads.
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
	Update(ctx context.Context, now int64, key string, fn UpdateFunc[T]) (Result, error)
	Delete(ctx context.Context, key string) error
}

// defaultShards is the number of independently locked shards a
// MapStore is built with unless WithShards says otherwise. It is
// comfortably above GOMAXPROCS on ordinary hardware, so concurrent
// callers rarely land on the same shard.
const defaultShards = 64

// MapStore is a KeyStore backed by in-memory maps, safe for
// concurrent use. It is the store to reach for by default; a
// distributed backend (Redis, ...) can implement the same interface
// without changing any algorithm. Its operations never block on I/O,
// so it ignores ctx and never returns a non-nil error of its own — it
// returns only what an UpdateFunc returns.
//
// Keys are spread across independently locked shards (64 unless set
// WithShards), so calls for different keys usually proceed in
// parallel and only calls colliding on one shard wait for each other.
// There is deliberately no lock covering the store as a whole.
//
// An expired entry reads as absent as soon as it expires. Its memory
// is reclaimed only if the store was built WithCleanupInterval: a
// background janitor then walks the shards one at a time, locking
// only the shard it is sweeping, and drops what has expired. The
// janitor has no clock of its own — it takes the latest now any
// Update has been called with — so a store that receives no calls at
// all is not swept until calls resume, and a shard that receives none
// may have its expired entries dropped up to one round late.
type MapStore[T any] struct {
	seed   maphash.Seed
	shards []shard[T]

	// stop and done are nil unless the janitor is running.
	stop      chan struct{}
	done      chan struct{}
	closeOnce sync.Once
	// sweepNow is the latest time the janitor has seen on any shard.
	// Only the janitor goroutine touches it.
	sweepNow int64
}

// MapStoreOption configures a MapStore built by NewMapStore.
type MapStoreOption func(*mapStoreConfig)

type mapStoreConfig struct {
	shards          int
	cleanupInterval time.Duration
}

// WithShards sets how many independently locked shards the store
// spreads its keys over; the default is 64. More shards mean calls
// for different keys wait on each other less often, and a shorter
// pause while the janitor sweeps a shard, since each shard holds fewer
// keys. Each shard costs a cache line plus an empty map. Consider
// raising it on machines with many more cores than 64, or for stores
// holding millions of keys; lowering it only saves memory in programs
// that create many stores.
//
// n must be a power of two, so that picking a key's shard is a mask
// rather than a division; WithShards panics otherwise.
func WithShards(n int) MapStoreOption {
	if err := validate.Shards(n); err != nil {
		panic(err)
	}
	return func(c *mapStoreConfig) {
		c.shards = n
	}
}

// WithCleanupInterval starts a background janitor that sweeps expired
// entries out of the store every interval. The store must then be
// closed with Close once it is no longer needed, or the janitor keeps
// running, and keeps the store reachable, for the life of the process.
//
// It panics if interval is not positive.
func WithCleanupInterval(interval time.Duration) MapStoreOption {
	if err := validate.CleanupInterval(interval); err != nil {
		panic(err)
	}
	return func(c *mapStoreConfig) {
		c.cleanupInterval = interval
	}
}

// NewMapStore returns an empty MapStore holding state of type T. T is
// the state type of whichever algorithm the store is for, so the call
// names it explicitly:
//
//	store := ratelimit.NewMapStore[algorithms.FixedWindowState](
//		ratelimit.WithCleanupInterval(time.Minute),
//	)
//	defer store.Close()
//	fw := algorithms.NewFixedWindowCounter(100, time.Minute, store)
//
// Without WithCleanupInterval, expired entries are never reclaimed; a
// long-lived process limiting on unbounded keys (per-IP, say) should
// always set it.
func NewMapStore[T any](opts ...MapStoreOption) *MapStore[T] {
	cfg := mapStoreConfig{shards: defaultShards}
	for _, opt := range opts {
		opt(&cfg)
	}

	s := &MapStore[T]{
		seed:   maphash.MakeSeed(),
		shards: make([]shard[T], cfg.shards),
	}
	for i := range s.shards {
		s.shards[i].init()
	}

	if cfg.cleanupInterval > 0 {
		s.stop = make(chan struct{})
		s.done = make(chan struct{})
		go s.runJanitor(cfg.cleanupInterval)
	}
	return s
}

// shardFor picks the shard owning key. len(s.shards) is a power of
// two, so the mask is equivalent to a modulo.
func (s *MapStore[T]) shardFor(key string) *shard[T] {
	return &s.shards[maphash.String(s.seed, key)&uint64(len(s.shards)-1)]
}

func (s *MapStore[T]) Update(_ context.Context, now int64, key string, fn UpdateFunc[T]) (Result, error) {
	return s.shardFor(key).update(now, key, fn)
}

func (s *MapStore[T]) Delete(_ context.Context, key string) error {
	s.shardFor(key).delete(key)
	return nil
}

// Close stops the background janitor, if the store has one, and waits
// for it to exit: once Close returns, the store does no background
// work. The store itself stays usable afterwards; it just no longer
// reclaims expired entries. Close is safe to call more than once and
// always returns nil.
//
// To tie the store's lifetime to a context instead:
//
//	context.AfterFunc(ctx, func() { store.Close() })
func (s *MapStore[T]) Close() error {
	if s.stop == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		close(s.stop)
		<-s.done
	})
	return nil
}

func (s *MapStore[T]) runJanitor(interval time.Duration) {
	defer close(s.done)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
			s.sweep(s.stop)
		}
	}
}

// sweep drops expired entries from every shard, one shard at a time,
// and returns early if stop is closed in between.
func (s *MapStore[T]) sweep(stop <-chan struct{}) {
	for i := range s.shards {
		select {
		case <-stop:
			return
		default:
		}
		s.sweepNow = s.shards[i].sweep(s.sweepNow)
	}
}
