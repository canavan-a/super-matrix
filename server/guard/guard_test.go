package guard

import (
	"testing"
	"time"
)

func TestRetryAfter(t *testing.T) {
	l := NewLimiter(1, time.Minute)
	t0 := time.Unix(1000, 0)
	if d := l.RetryAfter("a", t0); d != 0 {
		t.Fatalf("unknown key: got %v, want 0", d)
	}
	l.Allow("a", t0)
	if d := l.RetryAfter("a", t0.Add(58*time.Second+500*time.Millisecond)); d != 2*time.Second {
		t.Fatalf("got %v, want 2s", d)
	}
	if d := l.RetryAfter("a", t0.Add(time.Minute)); d != 0 {
		t.Fatalf("window over: got %v, want 0", d)
	}
}

func TestLimitKey(t *testing.T) {
	if a, b := LimitKey("2001:db8:1:2::1"), LimitKey("2001:db8:1:2:ffff::9"); a != b {
		t.Fatalf("same /64 got different keys: %s %s", a, b)
	}
	if a, b := LimitKey("2001:db8:1:2::1"), LimitKey("2001:db8:1:3::1"); a == b {
		t.Fatalf("different /64s share key %s", a)
	}
	if got := LimitKey("203.0.113.7"); got != "203.0.113.7" {
		t.Fatalf("ipv4 changed: %s", got)
	}
}

func TestSweepOncePerWindow(t *testing.T) {
	l := NewLimiter(1, time.Minute)
	t0 := time.Unix(1000, 0)
	for i := 0; i < 10001; i++ {
		l.Allow(string(rune(i+1))+"k", t0)
	}
	l.Allow("x", t0.Add(2*time.Minute)) // sweeps: everything expired
	if n := len(l.hits); n != 1 {
		t.Fatalf("after sweep: %d keys, want 1", n)
	}
}
