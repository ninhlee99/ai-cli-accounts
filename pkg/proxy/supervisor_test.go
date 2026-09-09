package proxy

import (
	"testing"
	"time"
)

func TestSuperBackoff(t *testing.T) {
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{0, 500 * time.Millisecond},
		{1, 1 * time.Second},
		{2, 2 * time.Second},
		{3, 4 * time.Second},
		{4, 8 * time.Second},
		{5, 8 * time.Second}, // capped
		{50, 8 * time.Second},
	}
	for _, c := range cases {
		if got := superBackoff(c.attempt); got != c.want {
			t.Errorf("superBackoff(%d) = %v, want %v", c.attempt, got, c.want)
		}
	}
}

func TestCrashLooping(t *testing.T) {
	now := time.Now()

	if crashLooping(nil, now) {
		t.Error("no history should not be crash-looping")
	}

	var recent []time.Time
	for i := 0; i < 4; i++ {
		recent = append(recent, now.Add(-time.Duration(i)*time.Second))
	}
	if crashLooping(recent, now) {
		t.Error("4 crashes within the window should not yet be crash-looping")
	}

	recent = append(recent, now.Add(-4*time.Second))
	if !crashLooping(recent, now) {
		t.Error("5 crashes within the window should be crash-looping")
	}

	var stale []time.Time
	for i := 0; i < 10; i++ {
		stale = append(stale, now.Add(-2*time.Minute))
	}
	if crashLooping(stale, now) {
		t.Error("crashes outside the window should not count")
	}
}
