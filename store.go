package ratelimit

import (
	"context"
	"sync"
)

// KeyStore stores per-key algorithm state under a caller-chosen
// string key (a user ID, an API key, an IP address, ...). A concrete
// Algorithm uses it to load and persist the state for one key
// without knowing how or where it's stored, so the storage strategy
// (in memory, Redis, ...) can be swapped without touching any
// algorithm's rate-limiting math. The stored value's concrete type
// is owned by whichever algorithm wrote it; a store only needs to
// hand it back unchanged.
//
// Every method takes a context.Context and returns an error, since an
// implementation backed by a network store (Redis, ...) can block on
// or fail the round trip; callers should propagate ctx for
// cancellation/timeouts and must not treat a non-nil err as "key not
// found" — MapStore's Get reports that distinctly via ok.
type KeyStore interface {
	Get(ctx context.Context, key string) (value any, ok bool, err error)
	Set(ctx context.Context, key string, value any) error
	Delete(ctx context.Context, key string) error
}

// MapStore is a KeyStore backed by an in-memory map, safe for
// concurrent use. It is the store to reach for by default; a
// distributed backend (Redis, ...) can implement the same interface
// without changing any algorithm. Its operations never block on I/O,
// so it ignores ctx and never returns a non-nil error.
type MapStore struct {
	mu    sync.Mutex
	items map[string]any
}

func NewMapStore() *MapStore {
	return &MapStore{items: make(map[string]any)}
}

func (r *MapStore) Get(_ context.Context, key string) (any, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	value, ok := r.items[key]
	return value, ok, nil
}

func (r *MapStore) Set(_ context.Context, key string, value any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items[key] = value
	return nil
}

func (r *MapStore) Delete(_ context.Context, key string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.items, key)
	return nil
}
