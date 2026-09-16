package ratelimit

import (
	"sync"
	"time"
	"unsafe"
)

const (
	// cacheLine is the size a shard pads itself to: the cache line size
	// on arm64, which x86-64's 64 divides evenly.
	cacheLine = 128

	// shardPad is what's left of a cache line after a shard's mutex,
	// map header, lastNow and peak.
	shardPad = cacheLine - 32

	// smallestCacheLine is the cache line size on x86-64, the smallest
	// among the platforms the padding is meant for.
	smallestCacheLine = 64

	// allocHeader is how far the start of the shards slice can sit past
	// a cache line boundary. The Go allocator prefixes an 8-byte header
	// to objects that contain pointers and are larger than 512 bytes
	// but small enough for a size class (up to 32 KiB) — a slice of 5
	// to 255 shards, including the default 64. Every other slice of
	// shards starts exactly on a boundary.
	allocHeader = 8

	// sweepGrace is how far behind the latest known time a sweep stays
	// when deciding what has expired. Callers read their clock before
	// they reach the store, so a call can arrive carrying a now
	// slightly older than one the shard has already seen; the grace
	// keeps a sweep from dropping state that such a call still needs.
	// update itself doesn't need it: it judges expiry by the caller's
	// own now.
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

// Padding only separates shards if the layout holds for every shard
// count WithShards allows. Two compile-time checks enforce it; a
// shard's layout doesn't depend on T, so any instantiation will do.
var (
	// Each shard is exactly one cache line, so shards follow one
	// another at cacheLine intervals whatever their number. This
	// fails to compile if a field is added to or removed from shard
	// without adjusting shardPad: the constant index is out of range
	// unless the difference is zero, and can't go negative for a
	// uintptr. (Written as an index rather than an array length
	// because some analyzers, GoLand's among them, don't treat
	// unsafe.Sizeof of an instantiated generic type as a constant and
	// would report the array form as a type mismatch.)
	_ = [1]struct{}{}[unsafe.Sizeof(shard[struct{}]{})-cacheLine]

	// The fields a call writes — everything before the padding, which
	// the check above pins to cacheLine-shardPad bytes — fit in one
	// line even on the smallest line size and with the slice shifted
	// by an allocation header, so they never share a line with the
	// next shard's. This fails to compile once they outgrow that.
	_ [smallestCacheLine - allocHeader - (cacheLine - shardPad)]struct{}
)

// entry is what a shard stores under a key: the algorithm's state
// together with the instant that state expires. Every key carries its
// own expiry.
type entry[T any] struct {
	value     T
	expiresAt int64
}

// shard is one independently locked part of a MapStore: a map of
// entries and the mutex guarding it. All access to a shard's fields
// goes through its methods, which take the lock themselves; MapStore
// only picks the shard.
type shard[T any] struct {
	mu    sync.Mutex
	items map[string]entry[T]
	// lastNow is the latest now update was called with on this shard —
	// the shard's notion of the current time, not any entry's expiry.
	// A sweep measures the entries' expiry against it instead of
	// reading a clock, so it always runs on the same clock as the
	// callers.
	lastNow int64
	// peak is the largest len(items) since items was last allocated —
	// roughly how many entries' worth of memory the map is holding.
	peak int
	// The padding keeps each shard's mutex on its own cache line, so
	// that a goroutine taking one shard's lock doesn't invalidate the
	// cache line holding a neighbouring shard's lock (false sharing).
	_ [shardPad]byte
}

// init prepares a zero shard for use. Shards live by value in one
// slice, so they are initialized in place rather than constructed and
// copied, which would copy the mutex.
func (sh *shard[T]) init() {
	sh.items = make(map[string]entry[T])
}

// update implements KeyStore.Update for the keys this shard owns.
func (sh *shard[T]) update(now int64, key string, fn UpdateFunc[T]) (Result, error) {
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

// delete removes key from the shard.
func (sh *shard[T]) delete(key string) {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	delete(sh.items, key)
}

// sweep drops the entries that expired by now — or by the shard's own
// latest time, if that is later — less sweepGrace, and rebuilds the
// map if it has shrunk well below its peak. It returns the time it
// used, for the next shard to start from.
func (sh *shard[T]) sweep(now int64) int64 {
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
