package algorithms_test

import (
	"context"
	"testing"
	"time"

	ratelimit "github.com/const-nash/go-rate-limiter"
	"github.com/const-nash/go-rate-limiter/algorithms"
)

func TestSlidingWindowLog_AllowsUpToLimit(t *testing.T) {
	ctx := context.Background()
	sw := algorithms.NewSlidingWindowLog(3, time.Minute, ratelimit.NewMapStore[algorithms.SlidingWindowState]())
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	for i := 0; i < 3; i++ {
		res, err := sw.Allow(ctx, now, "u1")
		if err != nil {
			t.Fatalf("request %d: unexpected error: %v", i, err)
		}
		if !res.Allowed {
			t.Fatalf("request %d: expected allowed, got denied", i)
		}
	}

	res, err := sw.Allow(ctx, now, "u1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Allowed {
		t.Fatal("expected 4th request within the same window to be denied")
	}
	if res.Remaining != 0 {
		t.Fatalf("expected 0 remaining, got %d", res.Remaining)
	}
}

func TestSlidingWindowLog_SlidesGradually(t *testing.T) {
	ctx := context.Background()
	sw := algorithms.NewSlidingWindowLog(2, time.Minute, ratelimit.NewMapStore[algorithms.SlidingWindowState]())
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	if res, err := sw.Allow(ctx, base, "u1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	} else if !res.Allowed {
		t.Fatal("expected first request to be allowed")
	}
	if res, err := sw.Allow(ctx, base.Add(30*time.Second), "u1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	} else if !res.Allowed {
		t.Fatal("expected second request to be allowed")
	}

	if res, err := sw.Allow(ctx, base.Add(59*time.Second), "u1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	} else if res.Allowed {
		t.Fatal("expected third request to be denied while both prior requests are still in the window")
	}

	// The request at t=0 ages out once now-window is past it, i.e. now > 1m.
	if res, err := sw.Allow(ctx, base.Add(61*time.Second), "u1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	} else if !res.Allowed {
		t.Fatal("expected request to be allowed once the oldest timestamp aged out")
	}

	// Unlike a fixed window, the window slid rather than resetting
	// entirely: the request at t=30s is still counted here.
	if res, err := sw.Allow(ctx, base.Add(62*time.Second), "u1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	} else if res.Allowed {
		t.Fatal("expected request to be denied since only one slot had freed up")
	}
}

func TestSlidingWindowLog_AllowN(t *testing.T) {
	ctx := context.Background()
	sw := algorithms.NewSlidingWindowLog(10, time.Minute, ratelimit.NewMapStore[algorithms.SlidingWindowState]())
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	res, err := sw.AllowN(ctx, now, "u1", 7)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Allowed || res.Remaining != 3 {
		t.Fatalf("expected allowed with 3 remaining, got allowed=%v remaining=%d", res.Allowed, res.Remaining)
	}

	res, err = sw.AllowN(ctx, now, "u1", 4)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Allowed {
		t.Fatal("expected request exceeding remaining quota to be denied")
	}
	if res.Remaining != 3 {
		t.Fatalf("expected remaining to stay at 3 on denial, got %d", res.Remaining)
	}
}

func TestSlidingWindowLog_RetryAfterAndResetAt(t *testing.T) {
	ctx := context.Background()
	sw := algorithms.NewSlidingWindowLog(1, time.Minute, ratelimit.NewMapStore[algorithms.SlidingWindowState]())
	now := time.Date(2026, 1, 1, 0, 0, 30, 0, time.UTC)

	if _, err := sw.Allow(ctx, now, "u1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	res, err := sw.Allow(ctx, now, "u1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantResetAt := now.Add(time.Minute)
	if !res.ResetAt.Equal(wantResetAt) {
		t.Fatalf("expected ResetAt %v, got %v", wantResetAt, res.ResetAt)
	}

	wantRetryAfter := wantResetAt.Sub(now)
	if res.RetryAfter != wantRetryAfter {
		t.Fatalf("expected RetryAfter %v, got %v", wantRetryAfter, res.RetryAfter)
	}
}

func TestSlidingWindowLog_IsolatesKeys(t *testing.T) {
	ctx := context.Background()
	sw := algorithms.NewSlidingWindowLog(1, time.Minute, ratelimit.NewMapStore[algorithms.SlidingWindowState]())
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	if res, err := sw.Allow(ctx, now, "u1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	} else if !res.Allowed {
		t.Fatal("expected first request for u1 to be allowed")
	}
	if res, err := sw.Allow(ctx, now, "u1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	} else if res.Allowed {
		t.Fatal("expected second request for u1 to be denied")
	}

	if res, err := sw.Allow(ctx, now, "u2"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	} else if !res.Allowed {
		t.Fatal("expected u2's own quota to be unaffected by u1's usage")
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
			algorithms.NewSlidingWindowLog(tt.limit, tt.window, ratelimit.NewMapStore[algorithms.SlidingWindowState]())
		})
	}
}
