package httpapi

import (
	"testing"
	"time"
)

func TestFixedWindowLimiter(t *testing.T) {
	now := time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC)
	limiter := newFixedWindowLimiter(2, time.Minute)
	limiter.now = func() time.Time { return now }

	if result := limiter.Take("client"); !result.Allowed || result.Remaining != 1 {
		t.Fatalf("unexpected first result: %+v", result)
	}
	if result := limiter.Take("client"); !result.Allowed || result.Remaining != 0 {
		t.Fatalf("unexpected second result: %+v", result)
	}
	if result := limiter.Take("client"); result.Allowed || result.RetryAfter != time.Minute {
		t.Fatalf("unexpected limited result: %+v", result)
	}

	limiter.Reset("client")
	if result := limiter.Take("client"); !result.Allowed {
		t.Fatal("reset should clear the window")
	}
}

func TestFixedWindowLimiterBoundsKeys(t *testing.T) {
	limiter := newFixedWindowLimiter(1, time.Minute)
	limiter.maxEntries = 1
	if result := limiter.Take("first"); !result.Allowed {
		t.Fatal("first key should be accepted")
	}
	if result := limiter.Take("second"); result.Allowed {
		t.Fatal("new key should be rejected when the limiter is at capacity")
	}
}
