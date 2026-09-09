package proxy

import "testing"

// TestRotator_EmptyProfiles exercises the Rotator against a profile
// directory with nothing saved in it — the state of a fresh install, or a
// machine where all profiles were removed. NewRotator/Load must not touch
// the system keychain (ListProfiles is empty, so profile.LoadClaudeToken is
// never called), which is what makes this testable without real macOS
// Keychain access; a Rotator test that actually exercises the keychain path
// is out of scope here.
func TestRotator_EmptyProfiles(t *testing.T) {
	t.Setenv("AM_HOME", t.TempDir())

	r := NewRotator("claude")

	if got := r.Active(); got != "" {
		t.Errorf("expected empty Active() with no profiles, got %q", got)
	}
	if got := r.Token(); got != "" {
		t.Errorf("expected empty Token() with no profiles, got %q", got)
	}
	if got := r.Names(); len(got) != 0 {
		t.Errorf("expected no profile names, got %v", got)
	}

	if err := r.ForceSwitch("nonexistent"); err == nil {
		t.Errorf("expected an error switching to a nonexistent profile")
	}

	status := r.Status()
	accts, ok := status["accounts"].([]map[string]any)
	if !ok {
		t.Fatalf("expected status[\"accounts\"] to be []map[string]any, got %T", status["accounts"])
	}
	if len(accts) != 0 {
		t.Errorf("expected 0 accounts in status, got %d", len(accts))
	}

	// Rotate/RefreshFromDisk/snapshotActiveIfChanged must be safe no-ops on
	// empty state rather than panicking on an out-of-range r.order[r.idx].
	r.Rotate("ghost-profile", "test")
	r.RefreshFromDisk()
	r.snapshotActiveIfChanged()
}
