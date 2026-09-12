package algorithms

import (
	"testing"
	"time"
)

func TestFixedWindowCounter_AllowsUpToLimit(t *testing.T) {
	fw := NewFixedWindowCounter(3, time.Minute)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	for i := 0; i < 3; i++ {
		res := fw.Allow(now)
		if !res.Allowed {
			t.Fatalf("request %d: expected allowed, got denied", i)
		}
	}

	res := fw.Allow(now)
	if res.Allowed {
		t.Fatal("expected 4th request within the same window to be denied")
	}
	if res.Remaining != 0 {
		t.Fatalf("expected 0 remaining, got %d", res.Remaining)
	}
}

func TestFixedWindowCounter_ResetsAfterWindowElapses(t *testing.T) {
	fw := NewFixedWindowCounter(1, time.Minute)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	res := fw.Allow(now)
	if !res.Allowed {
		t.Fatal("expected first request to be allowed")
	}

	if res := fw.Allow(now.Add(30 * time.Second)); res.Allowed {
		t.Fatal("expected second request within the same window to be denied")
	}

	res = fw.Allow(now.Add(time.Minute))
	if !res.Allowed {
		t.Fatal("expected request in the next window to be allowed")
	}
}

func TestFixedWindowCounter_AllowN(t *testing.T) {
	fw := NewFixedWindowCounter(10, time.Minute)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	res := fw.AllowN(now, 7)
	if !res.Allowed || res.Remaining != 3 {
		t.Fatalf("expected allowed with 3 remaining, got allowed=%v remaining=%d", res.Allowed, res.Remaining)
	}

	res = fw.AllowN(now, 4)
	if res.Allowed {
		t.Fatal("expected request exceeding remaining quota to be denied")
	}
	if res.Remaining != 3 {
		t.Fatalf("expected remaining to stay at 3 on denial, got %d", res.Remaining)
	}
}

func TestFixedWindowCounter_RetryAfterAndResetAt(t *testing.T) {
	fw := NewFixedWindowCounter(1, time.Minute)
	now := time.Date(2026, 1, 1, 0, 0, 30, 0, time.UTC)

	fw.Allow(now)
	res := fw.Allow(now)

	wantResetAt := now.Truncate(time.Minute).Add(time.Minute)
	if !res.ResetAt.Equal(wantResetAt) {
		t.Fatalf("expected ResetAt %v, got %v", wantResetAt, res.ResetAt)
	}

	wantRetryAfter := wantResetAt.Sub(now)
	if res.RetryAfter != wantRetryAfter {
		t.Fatalf("expected RetryAfter %v, got %v", wantRetryAfter, res.RetryAfter)
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
			NewFixedWindowCounter(tt.limit, tt.window)
		})
	}
}
