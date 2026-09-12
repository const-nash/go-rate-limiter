package algorithms_test

import (
	"testing"
	"time"

	"github.com/const-nash/go-rate-limiter/algorithms"
)

func TestSlidingWindowLog_AllowsUpToLimit(t *testing.T) {
	sw := algorithms.NewSlidingWindowLog(3, time.Minute)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	for i := 0; i < 3; i++ {
		res := sw.Allow(now)
		if !res.Allowed {
			t.Fatalf("request %d: expected allowed, got denied", i)
		}
	}

	res := sw.Allow(now)
	if res.Allowed {
		t.Fatal("expected 4th request within the same window to be denied")
	}
	if res.Remaining != 0 {
		t.Fatalf("expected 0 remaining, got %d", res.Remaining)
	}
}

func TestSlidingWindowLog_SlidesGradually(t *testing.T) {
	sw := algorithms.NewSlidingWindowLog(2, time.Minute)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	if res := sw.Allow(base); !res.Allowed {
		t.Fatal("expected first request to be allowed")
	}
	if res := sw.Allow(base.Add(30 * time.Second)); !res.Allowed {
		t.Fatal("expected second request to be allowed")
	}

	if res := sw.Allow(base.Add(59 * time.Second)); res.Allowed {
		t.Fatal("expected third request to be denied while both prior requests are still in the window")
	}

	// The request at t=0 ages out once now-window is past it, i.e. now > 1m.
	if res := sw.Allow(base.Add(61 * time.Second)); !res.Allowed {
		t.Fatal("expected request to be allowed once the oldest timestamp aged out")
	}

	// Unlike a fixed window, the window slid rather than resetting
	// entirely: the request at t=30s is still counted here.
	if res := sw.Allow(base.Add(62 * time.Second)); res.Allowed {
		t.Fatal("expected request to be denied since only one slot had freed up")
	}
}

func TestSlidingWindowLog_AllowN(t *testing.T) {
	sw := algorithms.NewSlidingWindowLog(10, time.Minute)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	res := sw.AllowN(now, 7)
	if !res.Allowed || res.Remaining != 3 {
		t.Fatalf("expected allowed with 3 remaining, got allowed=%v remaining=%d", res.Allowed, res.Remaining)
	}

	res = sw.AllowN(now, 4)
	if res.Allowed {
		t.Fatal("expected request exceeding remaining quota to be denied")
	}
	if res.Remaining != 3 {
		t.Fatalf("expected remaining to stay at 3 on denial, got %d", res.Remaining)
	}
}

func TestSlidingWindowLog_RetryAfterAndResetAt(t *testing.T) {
	sw := algorithms.NewSlidingWindowLog(1, time.Minute)
	now := time.Date(2026, 1, 1, 0, 0, 30, 0, time.UTC)

	sw.Allow(now)
	res := sw.Allow(now)

	wantResetAt := now.Add(time.Minute)
	if !res.ResetAt.Equal(wantResetAt) {
		t.Fatalf("expected ResetAt %v, got %v", wantResetAt, res.ResetAt)
	}

	wantRetryAfter := wantResetAt.Sub(now)
	if res.RetryAfter != wantRetryAfter {
		t.Fatalf("expected RetryAfter %v, got %v", wantRetryAfter, res.RetryAfter)
	}
}

func TestSlidingWindowLog_InvalidArgsPanic(t *testing.T) {
	tests := []struct {
		name   string
		limit  int
		window time.Duration
	}{
		{"zero limit", 0, time.Minute},
		{"negative limit", -1, time.Minute},
		{"zero window", 3, 0},
		{"negative window", 3, -time.Minute},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("expected panic for invalid arguments")
				}
			}()
			algorithms.NewSlidingWindowLog(tt.limit, tt.window)
		})
	}
}
