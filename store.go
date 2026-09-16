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

const (
	// defaultShards is the number of independently locked shards a
	// MapStore is built with. It is a power of two so a key's hash
	// maps to a shard with a mask rather than a division, and is
	// comfortably above GOMAXPROCS on ordinary hardware so that
	// concurrent callers rarely land on the same shard.
	defaultShards = 64

	// shardPad is what's left of a 128-byte cache line (the line size
	// on arm64; x86-64's 64 divides it evenly) after a shard's mutex,
	// map header, lastNow and peak.
	shardPad = 128 - 32

	// sweepGrace is how far behind the latest known time the janitor
	// stays when deciding what has expired. Callers read their clock
	// before they reach the store, so a call can arrive carrying a now
	// slightly older than one the store has already seen; the grace
	// keeps the janitor from dropping state that such a call still
	// needs. Update itself doesn't need it: it judges expiry by the
	// caller's own now.
	sweepGrace = int64(time.Second)

	// A sweep rebuilds a shard's map once it holds at most
	// 1/shrinkFactor of the entries it peaked at, provided that peak
	// was at least shrinkMinPeak. A Go map never gives memory back on
	// delete, so without the rebuild a burst of keys (a scan across
	// many IPs, say) would pin its peak size for good; below
	// shrinkMinPeak the memory at stake isn't worth the copy.
	shrinkFactor  = 4
	shrinkMinPeak = 1024
)

// entry is one stored state together with the instant it expires.
type entry[T any] struct {
	value     T
	expiresAt int64
}

// mapShard is one independently locked slice of a MapStore's keys.
type mapShard[T any] struct {
	mu    sync.Mutex
	items map[string]entry[T]
	// lastNow is the latest now any Update on this shard was called
	// with. The janitor measures expiry against it instead of reading
	// a clock, so it always runs on the same clock as the callers.
	lastNow int64
	// peak is the largest len(items) since items was last allocated —
	// roughly how many entries' worth of memory the map is holding.
	peak int
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
	shards []mapShard[T]

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
	cleanupInterval time.Duration
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
	var cfg mapStoreConfig
	for _, opt := range opts {
		opt(&cfg)
	}

	s := &MapStore[T]{
		seed:   maphash.MakeSeed(),
		shards: make([]mapShard[T], defaultShards),
	}
	for i := range s.shards {
		s.shards[i].items = make(map[string]entry[T])
	}

	if cfg.cleanupInterval > 0 {
		s.stop = make(chan struct{})
		s.done = make(chan struct{})
		go s.runJanitor(cfg.cleanupInterval)
	}
	return s
}

// shard picks the shard owning key. len(s.shards) is a power of two,
// so the mask is equivalent to a modulo.
func (s *MapStore[T]) shard(key string) *mapShard[T] {
	return &s.shards[maphash.String(s.seed, key)&uint64(len(s.shards)-1)]
}

func (s *MapStore[T]) Update(_ context.Context, now int64, key string, fn UpdateFunc[T]) (Result, error) {
	sh := s.shard(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	if now > sh.lastNow {
		sh.lastNow = now
	}

	var prev T
	e, ok := sh.items[key]
	if ok && e.expiresAt > now {
		prev = e.value
	} else {
		ok = false
	}

	next, expiresAt, out, err := fn(prev, ok)
	if err != nil {
		return Result{}, err
	}

	if expiresAt <= now {
		delete(sh.items, key)
		return out, nil
	}
	sh.items[key] = entry[T]{value: next, expiresAt: expiresAt}
	if n := len(sh.items); n > sh.peak {
		sh.peak = n
	}
	return out, nil
}

func (s *MapStore[T]) Delete(_ context.Context, key string) error {
	sh := s.shard(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	delete(sh.items, key)
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

// sweep drops the shard's entries that expired by now — or by the
// shard's own latest time, if that is later — less sweepGrace, and
// rebuilds the map if it has shrunk well below its peak. It returns
// the time it used, for the next shard to start from.
func (sh *mapShard[T]) sweep(now int64) int64 {
	sh.mu.Lock()
	defer sh.mu.Unlock()

	now = max(now, sh.lastNow)
	cutoff := now - sweepGrace
	for key, e := range sh.items {
		if e.expiresAt <= cutoff {
			delete(sh.items, key)
		}
	}

	if live := len(sh.items); sh.peak >= shrinkMinPeak && live <= sh.peak/shrinkFactor {
		items := make(map[string]entry[T], live)
		for key, e := range sh.items {
			items[key] = e
		}
		sh.items = items
		sh.peak = live
	}
	return now
}
