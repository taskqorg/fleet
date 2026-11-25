package main

import (
	"testing"
	"time"
)

func withResourceRetry(t *testing.T, base, maximum time.Duration) {
	t.Helper()
	b, m := kitRetryBase, kitRetryMax
	kitRetryBase, kitRetryMax = base, maximum
	t.Cleanup(func() { kitRetryBase, kitRetryMax = b, m })
}

// Requirement: a failing item pauses exponentially with jitter up to the maximum, reproducibly for a seed; risk: equal
// failures retry in step, or the pause grows without a ceiling.
func TestResourceRetryDelay(t *testing.T) {
	withResourceRetry(t, 100*time.Millisecond, 5*time.Minute)
	k := newWorkKit(&App{}, time.Now, 7)
	for n := 0; n < 80; n++ {
		step := min(5*time.Minute, 100*time.Millisecond<<min(n, 30))
		for range 20 {
			low := step
			if step == 5*time.Minute {
				low = min(step, 5*time.Minute*3/4) // at the maximum the jitter goes below it
			}
			if d := k.backoff(n); d < low || d > min(5*time.Minute, step*3/2) {
				t.Fatalf("delay(%d) = %v, step %v", n, d, step)
			}
		}
	}
	a, b := newWorkKit(&App{}, time.Now, 9), newWorkKit(&App{}, time.Now, 9)
	for n := 0; n < 20; n++ {
		if x, y := a.backoff(n), b.backoff(n); x != y {
			t.Fatalf("same seed, different jitter: %v %v", x, y)
		}
	}
	if n := kitFailingAttempts(); n != 13 { // 100 ms * 2^12 is the first step at 5 minutes
		t.Fatalf("failing attempts %d", n)
	}
	for d, want := range map[time.Duration]string{time.Minute: "1 minute", 90 * time.Second: "1.5 minutes", 5 * time.Minute: "5 minutes", time.Second: "1 second", 800 * time.Millisecond: "0.8 seconds"} {
		withResourceRetry(t, time.Millisecond, d)
		if got := kitRetryEvery(); got != want {
			t.Fatalf("every %v = %q", d, got)
		}
	}
	withResourceRetry(t, 100*time.Millisecond, 110*time.Millisecond)
	for range 20 {
		if d := k.backoff(0); d < 100*time.Millisecond || d > 110*time.Millisecond {
			t.Fatalf("uncapped step jitter exceeded its bounds: %v", d)
		}
	}
	withResourceRetry(t, time.Second, time.Second)
	if n, d := kitFailingAttempts(), k.backoff(5); n != 1 || d < 750*time.Millisecond || d > time.Second {
		t.Fatalf("base at the maximum: failing attempts %d, delay %v", n, d)
	}
}
