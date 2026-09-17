# Benchmarks

Every performance claim about this library is supposed to come from
these benchmarks, and every change to the hot path — `Limiter.Allow`,
an `Algorithm`, `KeyStore.Update`, `shard` — is supposed to be measured
against them before and after. They exist because the decisions behind
the current design (atomicity inside the store, sharding, `int64`
time, expiry) were each made from a measurement, and without a
committed suite nothing would catch them regressing.

Like the tests, benchmarks live in external `_test` packages and go
through the public API only.

## Layout

| File | What it covers |
| --- | --- |
| `algorithms/bench_test.go` | The scenarios **every** algorithm runs, as a table over implementations, so their numbers are comparable. Also the shared helpers: `benchKeys`, `latentStore`, `runParallel`, `heapInUse`. |
| `algorithms/fixed_window_bench_test.go` | What is peculiar to `FixedWindowCounter`: window rollover, first call for a key, `AllowN` with larger `n`. |
| `algorithms/sliding_window_log_bench_test.go` | What is peculiar to `SlidingWindowLog`: steady state with eviction, discarding a whole log, `O(limit)` memory. |
| `store_bench_test.go` | `MapStore` on its own: shard-count sweep, pauses while the janitor sweeps, memory per key, and `SystemClock`. |

A new algorithm adds itself to `algorithmCases` in
`algorithms/bench_test.go` — two constructors, one plain and one
backed by `latentStore` — and gets the whole shared set for free. Its
own file is for what only it does.

## Running them

The `Makefile` wraps all of this, so an IDE can run any of it as a
one-click configuration (`make` on its own lists every target):

```
make bench-smoke     # every benchmark once, just to check it still works
make bench           # the real thing: -count 10 -cpu 1,8
make bench-compare   # working tree vs BASE, through benchstat
```

Each takes the same knobs as the commands below, as variables:

```
make bench BENCH=SlidingWindow CPU=8 COUNT=3 BENCHTIME=200000x
make bench-compare BASE=HEAD~1
```

`bench-compare` needs the benchmarks to exist in `BASE` as well — it
checks and says so if they don't. What the targets do underneath, and
why, is the rest of this section.

Benchmarks don't run as part of `go test`, since `-bench` defaults to
matching nothing. To run them all once, just to check they work:

```
go test -run '^$' -bench . -benchtime 1x ./...
```

For numbers worth reading, several rounds at one and many cores:

```
go test -run '^$' -bench . -benchmem -count 10 -cpu 1,8 ./... > new.txt
```

A single round means nothing here: the spread between rounds on an
otherwise idle laptop reaches ±20%. Compare with `benchstat`, which
reports medians and says whether a difference is significant at all:

```
go install golang.org/x/perf/cmd/benchstat@latest
benchstat old.txt new.txt
```

To produce `old.txt` from the code before a change without disturbing
the working tree, check the base commit out into a worktree of its own:

```
git worktree add ../go-rate-limiter-base HEAD
(cd ../go-rate-limiter-base && go test -run '^$' -bench . -benchmem -count 10 -cpu 1,8 ./... > /tmp/old.txt)
git worktree remove ../go-rate-limiter-base
```

Run the two sets back to back on the same machine, ideally
interleaved. Numbers from different sessions are not comparable — a
figure from last week's notes is a hint, not a baseline.

In CI, run the suite with `-benchtime 1x` as a smoke test. Comparing
timings on shared CI hardware is not worth the false alarms.

## The scenarios, and what each is for

Shared by every algorithm:

- **`SerialOneKey`** — one uncontended call. The number to watch for
  allocations per request.
- **`ParallelManyKeys`** (`-cpu 1,8`) — scaling with cores when keys
  are spread out. Before atomicity moved into the store this got
  *slower* as cores were added; that regression is what this catches.
- **`ParallelHotKey`** — every call for a single key. Calls for one key
  serialize on that key's shard, and no amount of sharding changes
  that, so this is a floor, not a defect.
- **`Denied`** — the quota is spent, so nothing is written and the
  answer is no. A separate branch worth its own number.
- **`SlowStore`** — throughput (`req/s`) against a store that costs a
  round trip, the shape a Redis-backed store has. Built on
  `time.Sleep`, so it is far noisier than the rest: read it for orders
  of magnitude. It guards the worst regression in the library's
  history, when a lock held across the round trip capped the whole
  limiter at ~800 req/s regardless of cores.
- **`MemoryPerKey`** — bytes one more tracked key costs, key string
  included. Decides whether a per-IP limiter fits in RAM.

`MapStore`'s own:

- **`UpdateParallel/shards=N`** — what `WithShards` buys; `shards=1` is
  the single-mutex store we replaced.
- **`PausesDuringCleanup`** — latency, not throughput. The janitor
  holds one shard's lock while sweeping it, so it reports `p99-ns` and
  `max-ns` for calls made while it runs; the average would hide
  exactly the spike worth knowing about.

## Pitfalls these benchmarks already account for

- **State is prepared before the timer**, or the numbers include a map
  growing rather than the operation under test.
- **Parallel goroutines don't share a counter.** Each gets its own
  offset into the key set; a shared atomic would measure itself.
- **A scenario has to be in the state its name claims.** A sliding
  window log on a fixed `now` fills up after `limit` calls and then
  only measures denials, which is why `SteadyState` walks time forward
  one slot per call so that every iteration both evicts and appends.
- **Heap measurements need the data kept alive.** `runtime.KeepAlive`
  after the measurement; otherwise the store is unreachable by the
  time `heapInUse` runs, and the result comes out near zero (it came
  out *negative* in the first draft of these benchmarks).
- **`b.Loop()`** rather than `for i := 0; i < b.N; i++`: it keeps
  setup out of the timing and stops the compiler from discarding the
  call whose result is unused. It needs Go 1.24+, which is why the
  module requires 1.25.

## Baseline

Recorded 2026-09-17, Apple M4 Max, Go 1.26.5, `-count 5`, medians.
Compare against numbers you take yourself on your own machine, not
against this table.

| Benchmark | 1 core | 8 cores | per op |
| --- | --- | --- | --- |
| `Algorithms_SerialOneKey/FixedWindowCounter` | 44.6 ns | 35.5 ns | 32 B, 1 alloc |
| `Algorithms_SerialOneKey/SlidingWindowLog` | 41.7 ns | 39.0 ns | 76 B, 1 alloc |
| `Algorithms_ParallelManyKeys/FixedWindowCounter` | 46.6 ns | 26.9 ns | 32 B, 1 alloc |
| `Algorithms_ParallelManyKeys/SlidingWindowLog` | 55.7 ns | 34.5 ns | 59 B, 1 alloc |
| `Algorithms_ParallelHotKey/FixedWindowCounter` | 39.7 ns | 158.8 ns | 32 B, 1 alloc |
| `Algorithms_Denied/FixedWindowCounter` | 41.8 ns | 36.8 ns | 32 B, 1 alloc |
| `MapStore_UpdateSerial` | 26.1 ns | 21.9 ns | 0 B, 0 allocs |
| `MapStore_UpdateParallel/shards=1` | 25.1 ns | 161.3 ns | 0 B, 0 allocs |
| `MapStore_UpdateParallel/shards=64` | 25.7 ns | 20.9 ns | 0 B, 0 allocs |
| `MapStore_UpdateParallel/shards=256` | 33.2 ns | 19.0 ns | 0 B, 0 allocs |
| `SystemClock_Now` | 10.8 ns | 11.2 ns | — |

Memory per key: 79 B for `FixedWindowCounter` and for `MapStore`
itself; `SlidingWindowLog` costs 217 B, 1113 B and 10329 B at limits
of 10, 100 and 1000 — the `O(limit)` it trades for exactness.

Two of these are worth reading together. `shards=1` at eight cores
(161 ns) against `shards=64` (21 ns) is the whole case for sharding.
`ParallelHotKey` at eight cores (159 ns) is the same contention
arriving through a single key, where sharding cannot help.

The one allocation per call in every algorithm row is the closure
passed to `Update`, which escapes because `KeyStore` is an interface;
`MapStore`'s own rows show zero, so the store itself allocates
nothing. Removing that last allocation is an open idea, and these
benchmarks are how it would be judged.
