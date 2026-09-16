package algorithms_test

import "time"

// epoch is the fixed instant tests measure time from.
var epoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// at returns epoch+offset in Unix nanoseconds, the form every
// Algorithm takes its now in.
func at(offset time.Duration) int64 {
	return epoch.Add(offset).UnixNano()
}

// fmtNano renders a Unix-nanosecond instant readably for failure
// messages.
func fmtNano(ns int64) string {
	return time.Unix(0, ns).UTC().Format(time.RFC3339Nano)
}
