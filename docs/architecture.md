# Architecture

The library is built around the **Strategy** pattern: `Limiter` does
not know how the limit is actually computed — it delegates that work
to the `Algorithm` interface. A concrete algorithm (token bucket,
leaky bucket, fixed window, sliding window log, etc.) is passed into
the limiter from the outside, via the `New` constructor. This makes
it possible to add new algorithms without changing `Limiter`, and to
swap the algorithm for a mock implementing `Algorithm` in tests.

`Limiter` also owns the `Clock` used to read the current time. It
reads `now` once per call and passes it explicitly into the
algorithm's methods, so every `Algorithm` implementation is a pure
function of `now` — it never reads the wall clock itself.

`Allow`/`AllowN` return a `Result` rather than a plain `bool`. A bare
boolean can't tell a caller how long to wait before retrying or how
much quota is left, which is what a production caller (an HTTP
handler returning `429` with a `Retry-After` header, a client doing
backoff) actually needs. `Result` carries `Allowed` plus the detail
each concrete algorithm can compute from its own state: `Remaining`,
`RetryAfter`, `ResetAt`, and `Limit`.

```mermaid
classDiagram
    class Algorithm {
        <<interface>>
        +Allow(now time.Time) Result
        +AllowN(now time.Time, n int) Result
    }

    class Result {
        +Allowed bool
        +Remaining int
        +RetryAfter time.Duration
        +ResetAt time.Time
        +Limit int
    }

    class Limiter {
        -algorithm Algorithm
        -clock Clock
        +New(algorithm Algorithm, clock Clock) *Limiter
        +Allow() Result
        +AllowN(n int) Result
    }

    class Clock {
        <<interface>>
        +Now() time.Time
    }

    class SystemClock {
        +Now() time.Time
    }

    class validate {
        <<utility>>
        +Rate(rate float64) error
        +Window(window time.Duration) error
    }

    class TokenBucket {
        -rate float64
        -burst int
        -tokens float64
        -lastRefill time.Time
        -mu sync.Mutex
        +NewTokenBucket(rate float64, burst int) *TokenBucket
        +Allow(now time.Time) Result
        +AllowN(now time.Time, n int) Result
    }

    class LeakyBucket {
        -rate float64
        -capacity int
        -level float64
        -lastLeak time.Time
        -mu sync.Mutex
        +NewLeakyBucket(rate float64, capacity int) *LeakyBucket
        +Allow(now time.Time) Result
        +AllowN(now time.Time, n int) Result
    }

    class FixedWindowCounter {
        -limit int
        -window time.Duration
        -count int
        -windowStart time.Time
        -mu sync.Mutex
        +NewFixedWindowCounter(limit int, window time.Duration) *FixedWindowCounter
        +Allow(now time.Time) Result
        +AllowN(now time.Time, n int) Result
    }

    class SlidingWindowLog {
        -limit int
        -window time.Duration
        -timestamps []time.Time
        -mu sync.Mutex
        +NewSlidingWindowLog(limit int, window time.Duration) *SlidingWindowLog
        +Allow(now time.Time) Result
        +AllowN(now time.Time, n int) Result
    }

    Limiter o-- Algorithm : holds (composition)
    Algorithm ..> Result : returns
    Limiter o-- Clock : holds (composition)
    Algorithm <|.. TokenBucket : implements
    Algorithm <|.. LeakyBucket : implements
    Algorithm <|.. FixedWindowCounter : implements
    Algorithm <|.. SlidingWindowLog : implements

    Clock <|.. SystemClock : implements

    TokenBucket ..> validate : calls in constructor
    LeakyBucket ..> validate : calls in constructor
    FixedWindowCounter ..> validate : calls in constructor
    SlidingWindowLog ..> validate : calls in constructor
```

## Shared infrastructure

The concrete algorithms don't share configuration parameters (each
one interprets "rate" in its own shape — `rate+burst` vs
`limit+window`), but two things are shared across the whole design:

- **`Clock`** — a small interface (`Now() time.Time`). Unlike
  `rate`/`limit`/`window`, "current time" means exactly the same
  thing for every algorithm, so it doesn't belong to any specific
  algorithm — it's a required parameter of `Limiter`'s constructor
  (`New(algorithm, clock)`), not of each algorithm's constructor.
  `Limiter` reads `clock.Now()` once per call and passes it down as
  an explicit `now` argument to `Allow`/`AllowN`. Because time
  arrives as a plain argument, every algorithm is trivial to
  unit-test on its own — just call `Allow(fixedTime)`, no fake clock
  or mock needed. In production code the caller passes the library's
  `SystemClock`; there is no implicit default.
- **`validate`** — a small set of package-level helper functions
  (`validate.Rate`, `validate.Window`, ...) that each algorithm's
  constructor calls to reject invalid input (`rate <= 0`,
  `window <= 0`, etc.). This avoids duplicating the same validation
  logic in every constructor without coupling the algorithms to each
  other or to `Limiter`.

`Limiter` still knows nothing about `rate`, `window`, or any other
algorithm-specific parameter — those stay owned by the concrete
algorithm.

## Package layout

Concrete algorithms live in their own package, `algorithms`, separate
from the root `ratelimit` package that defines `Algorithm`, `Result`,
`Limiter`, and `Clock`:

```mermaid
flowchart TB
    subgraph root["ratelimit (root package)"]
        Algorithm
        Result
        Limiter
        Clock
        SystemClock
    end
    subgraph algs["ratelimit/algorithms"]
        FixedWindowCounter
        TokenBucket
        LeakyBucket
        SlidingWindowLog
    end
    subgraph iv["ratelimit/internal/validate"]
        validate
    end
    algs -->|imports for Algorithm, Result| root
    algs -->|imports| iv
```

`algorithms` imports `ratelimit` to implement `Algorithm` and return
`Result`; `ratelimit` never imports `algorithms` back, so there's no
import cycle — this is the same shape as `image` and `image/png` in
the standard library, or `hash` and `crypto/sha256`. It also keeps
the Strategy pattern honest: `Limiter` depends only on the
`Algorithm` interface, never on a concrete type, so nothing stops a
caller from implementing `Algorithm` outside this module entirely.

`internal/validate` stays a separate, unexported package rather than
living inside `algorithms`, since it's shared infrastructure that any
future algorithm package — inside or outside `algorithms` — should be
able to call without depending on unrelated algorithm code.

## Usage flow

The client picks and creates a concrete algorithm implementation,
then passes it — together with a `Clock` — into `Limiter` via `New`.
From that point on, the client only calls `Limiter.Allow()`;
`Limiter` reads the current time itself, forwards it into the
algorithm, and returns the `Result` the algorithm computed.

```mermaid
flowchart LR
    A["client code"] -->|"algorithms.NewTokenBucket(rate, burst)"| B["TokenBucket\n(Algorithm)"]
    A -->|"ratelimit.New(algorithm, clock)"| C["Limiter"]
    A -->|"SystemClock{}"| E["Clock"]
    B -.->|"passed in as\nAlgorithm"| C
    E -.->|"passed in as\nClock"| C
    C -->|"now := clock.Now()"| E
    C -->|"algorithm.Allow(now)"| B
    B -->|"Result{Allowed, RetryAfter, ...}"| C
    C -->|"Result"| A
```

## Extending with a new algorithm

To add a new rate-limiting algorithm:

1. Create a struct in the `algorithms` package holding its own
   internal state (counters, timestamps, a mutex for concurrent
   access). It does not need a `Clock` field — time is received per
   call, not stored.
2. Implement `Allow(now time.Time) ratelimit.Result` and
   `AllowN(now time.Time, n int) ratelimit.Result` on it, satisfying
   the `ratelimit.Algorithm` interface. Fill in whichever `Result`
   fields the algorithm can meaningfully compute from its own state
   (at least `Allowed`; typically `RetryAfter` and `Remaining` too).
3. Validate its constructor arguments using the shared
   `internal/validate` helpers.
4. Pass an instance of the new struct, together with a `Clock`
   (typically `ratelimit.SystemClock`), into
   `ratelimit.New(algorithm, clock)`.

`Limiter` and the rest of the root package remain unchanged.
