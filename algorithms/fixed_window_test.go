package algorithms_test

import (
	"context"
	"testing"
	"time"

	ratelimit "github.com/const-nash/go-rate-limiter"
	"github.com/const-nash/go-rate-limiter/algorithms"
)

func TestFixedWindowCounter_AllowsUpToLimit(t *testing.T) {
	ctx := context.Background()
	fw := algorithms.NewFixedWindowCounter(3, time.Minute, ratelimit.NewMapStore[algorithms.FixedWindowState]())
	now := at(0)

	for i := 0; i < 3; i++ {
		res, err := fw.Allow(ctx, now, "u1")
		if err != nil {
			t.Fatalf("request %d: unexpected error: %v", i, err)
		}
		if !res.Allowed {
			t.Fatalf("request %d: expected allowed, got denied", i)
		}
	}

	res, err := fw.Allow(ctx, now, "u1")
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

func TestFixedWindowCounter_ResetsAfterWindowElapses(t *testing.T) {
	ctx := context.Background()
	fw := algorithms.NewFixedWindowCounter(1, time.Minute, ratelimit.NewMapStore[algorithms.FixedWindowState]())
	now := at(0)

	res, err := fw.Allow(ctx, now, "u1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Allowed {
		t.Fatal("expected first request to be allowed")
	}

	if res, err := fw.Allow(ctx, at(30*time.Second), "u1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	} else if res.Allowed {
		t.Fatal("expected second request within the same window to be denied")
	}

	res, err = fw.Allow(ctx, at(time.Minute), "u1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Allowed {
		t.Fatal("expected request in the next window to be allowed")
	}
}

func TestFixedWindowCounter_AllowN(t *testing.T) {
	ctx := context.Background()
	fw := algorithms.NewFixedWindowCounter(10, time.Minute, ratelimit.NewMapStore[algorithms.FixedWindowState]())
	now := at(0)

	res, err := fw.AllowN(ctx, now, "u1", 7)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Allowed || res.Remaining != 3 {
		t.Fatalf("expected allowed with 3 remaining, got allowed=%v remaining=%d", res.Allowed, res.Remaining)
	}

	res, err = fw.AllowN(ctx, now, "u1", 4)
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

func TestFixedWindowCounter_RetryAfterAndResetAt(t *testing.T) {
	ctx := context.Background()
	fw := algorithms.NewFixedWindowCounter(1, time.Minute, ratelimit.NewMapStore[algorithms.FixedWindowState]())
	now := at(30 * time.Second)

	if _, err := fw.Allow(ctx, now, "u1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	res, err := fw.Allow(ctx, now, "u1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantResetAt := at(time.Minute)
	if res.ResetAt != wantResetAt {
		t.Fatalf("expected ResetAt %v, got %v", fmtNano(wantResetAt), fmtNano(res.ResetAt))
	}

	wantRetryAfter := 30 * time.Second
	if res.RetryAfter != wantRetryAfter {
		t.Fatalf("expected RetryAfter %v, got %v", wantRetryAfter, res.RetryAfter)
	}
}

func TestFixedWindowCounter_IsolatesKeys(t *testing.T) {
	ctx := context.Background()
	fw := algorithms.NewFixedWindowCounter(1, time.Minute, ratelimit.NewMapStore[algorithms.FixedWindowState]())
	now := at(0)

	if res, err := fw.Allow(ctx, now, "u1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	} else if !res.Allowed {
		t.Fatal("expected first request for u1 to be allowed")
	}
	if res, err := fw.Allow(ctx, now, "u1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	} else if res.Allowed {
		t.Fatal("expected second request for u1 to be denied")
	}

	if res, err := fw.Allow(ctx, now, "u2"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	} else if !res.Allowed {
		t.Fatal("expected u2's own quota to be unaffected by u1's usage")
	}
}

func TestFixedWindowCounter_InvalidArgsPanic(t *testing.T) {
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
			algorithms.NewFixedWindowCounter(tt.limit, tt.window, ratelimit.NewMapStore[algorithms.FixedWindowState]())
		})
	}
}

func TestFixedWindowCounter_ExpiresAtWindowEnd(t *testing.T) {
	ctx := context.Background()
	store := newExpirySpy[algorithms.FixedWindowState]()
	fw := algorithms.NewFixedWindowCounter(1, time.Minute, store)

	if _, err := fw.Allow(ctx, at(10*time.Second), "u1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := at(time.Minute); store.lastExpiresAt != want {
		t.Fatalf("allowed: expected expiry at window end %v, got %v", fmtNano(want), fmtNano(store.lastExpiresAt))
	}

	if res, err := fw.Allow(ctx, at(20*time.Second), "u1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	} else if res.Allowed {
		t.Fatal("expected second request within the window to be denied")
	}
	if want := at(time.Minute); store.lastExpiresAt != want {
		t.Fatalf("denied: expected expiry at window end %v, got %v", fmtNano(want), fmtNano(store.lastExpiresAt))
	}
}
