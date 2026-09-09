package proxy

import (
	"os"
	"testing"
)

func TestLifecycle_RejectsInvalidPID(t *testing.T) {
	l := NewLifecycle()
	l.AddSession(0)
	l.AddSession(-1)
	if got := l.Sessions(); got != 0 {
		t.Fatalf("expected 0 sessions for invalid pid, got %d", got)
	}
}

func TestLifecycle_AddEndSession(t *testing.T) {
	l := NewLifecycle()
	// Our own pid: guaranteed alive and, unlike pid 1, guaranteed signalable
	// (kill(1, 0) needs root on macOS and fails with EPERM otherwise, which
	// pruneDead can't distinguish from "process doesn't exist").
	pid := os.Getpid()
	l.AddSession(pid)
	if got := l.Sessions(); got != 1 {
		t.Fatalf("expected 1 session after AddSession, got %d", got)
	}
	l.EndSession(pid)
	if got := l.Sessions(); got != 0 {
		t.Fatalf("expected 0 sessions after EndSession, got %d", got)
	}
}

func TestLifecycle_PruneDeadPID(t *testing.T) {
	l := NewLifecycle()
	// A pid astronomically unlikely to be alive on any real system.
	const deadPID = 1 << 30
	l.AddSession(deadPID)
	if got := l.Sessions(); got != 0 {
		t.Fatalf("expected dead pid to be pruned, got %d session(s)", got)
	}
}

// TestLifecycle_EPERMDoesNotCountAsDead regression-tests the EPERM/ESRCH
// conflation bug: pruneDead used to delete a pid on *any* non-nil error
// from Signal(0), including EPERM ("process exists, but you can't signal
// it" — e.g. it's owned by another user), which is not the same as ESRCH
// ("no such process"). pid 1 (init/launchd) always exists and, unless this
// test happens to run as root, signaling it returns EPERM rather than nil
// — exactly the case the old code mishandled. If it does run as root,
// Signal(0) just succeeds instead; either way pid 1 is alive and must not
// be pruned, so the assertion holds regardless of privilege level.
func TestLifecycle_EPERMDoesNotCountAsDead(t *testing.T) {
	l := NewLifecycle()
	l.AddSession(1)
	if got := l.Sessions(); got != 1 {
		t.Fatalf("expected pid 1 (alive, permission-denied signal) to remain tracked, got %d session(s)", got)
	}
}
