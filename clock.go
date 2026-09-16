package ratelimit

import "time"

// Clock reports the current time as nanoseconds since the Unix epoch.
//
// Time is an int64 throughout the library rather than a time.Time: a
// time.Time is 24 bytes and every algorithm stores at least one per
// key (the sliding window log stores one per request), while an int64
// is 8. Durations stay time.Duration, which is already an int64 count
// of nanoseconds, so now + int64(window) needs no conversion.
//
// A Limiter reads its Clock once per call and passes that value down
// to the algorithm and from there to its KeyStore, so every timestamp
// a store ever sees comes from the same Clock.
type Clock interface {
	Now() int64
}

// SystemClock is the production Clock. It never goes backwards.
//
// Plain wall-clock time (time.Now().UnixNano()) can: an NTP step or a
// manual clock change moves it, and a jump backwards stretches a
// rate-limiting window while a jump forwards cuts it short.
// SystemClock instead reads the wall clock once, when the package is
// initialized, and from then on adds the time elapsed on the monotonic
// clock. Its values still read as Unix time, so fixed windows keep
// lining up with round wall-clock boundaries, but a clock step inside
// the process doesn't move them.
//
// The flip side is that it doesn't follow a deliberate correction of
// the wall clock either: a process whose clock was off at startup
// stays off until it restarts. On Linux the monotonic clock also
// stands still while the machine is suspended, so after a suspend
// SystemClock lags the wall clock by the suspended time. Processes
// sharing a distributed KeyStore agree on time only as well as their
// wall clocks agreed when each of them started.
type SystemClock struct{}

var (
	// clockBase is the instant SystemClock counts from. It carries a
	// monotonic reading, which is what time.Since measures against.
	clockBase = time.Now()
	// clockBaseUnix is clockBase on the wall clock, in Unix nanoseconds.
	clockBaseUnix = clockBase.UnixNano()
)

func (SystemClock) Now() int64 {
	return clockBaseUnix + int64(time.Since(clockBase))
}
