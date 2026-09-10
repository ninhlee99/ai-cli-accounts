package proxy

import (
	"testing"
	"time"

	"amux-accounts/pkg/types"
)

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

func TestParseUsedThreshold(t *testing.T) {
	tests := []struct {
		in   float64
		want float64
	}{
		{95, 0.95},
		{0.95, 0.95},
		{90, 0.90},
		{0, DefaultUsedThreshold},
		{-1, DefaultUsedThreshold},
		{101, DefaultUsedThreshold},
		{1, 1},
	}
	for _, tt := range tests {
		got := ParseUsedThreshold(tt.in)
		if got != tt.want {
			t.Errorf("ParseUsedThreshold(%v) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestRotator_AllUnavailable(t *testing.T) {
	t.Setenv("AM_HOME", t.TempDir())
	r := NewRotator("claude")
	if !r.AllUnavailable() {
		t.Fatal("empty rotator should be AllUnavailable")
	}
}

func TestRotator_ShouldFailoverToProviderPool(t *testing.T) {
	t.Setenv("AM_HOME", t.TempDir())

	// 0 profiles → false (no-token path handles empty)
	r0 := NewRotator("claude")
	if r0.ShouldFailoverToProviderPool() {
		t.Fatal("0 profiles: should not failover via ShouldFailover")
	}

	// 1 profile, available → false
	r1 := &Rotator{
		tool:          "claude",
		order:         []string{"solo"},
		tokens:        map[string]*types.Token{"solo": {Access: "tok"}},
		accounts:      map[string]string{},
		cooldown:      map[string]time.Time{},
		dead:          map[string]bool{},
		autoSwitches:  map[string]int{},
		manualSwitches: map[string]int{},
		usedThreshold: DefaultUsedThreshold,
	}
	if r1.ShouldFailoverToProviderPool() {
		t.Fatal("1 available profile: no failover")
	}

	// 1 profile, cooling → true
	r1.cooldown["solo"] = time.Now().Add(time.Hour)
	if !r1.ShouldFailoverToProviderPool() {
		t.Fatal("1 cooling profile: expect failover")
	}

	// 2 profiles, both cooling → true (exhaust Claude first, then pool)
	r2 := &Rotator{
		tool:   "claude",
		order:  []string{"a", "b"},
		tokens: map[string]*types.Token{"a": {Access: "t"}, "b": {Access: "t"}},
		accounts: map[string]string{},
		cooldown: map[string]time.Time{
			"a": time.Now().Add(time.Hour),
			"b": time.Now().Add(time.Hour),
		},
		dead:           map[string]bool{},
		autoSwitches:   map[string]int{},
		manualSwitches: map[string]int{},
		usedThreshold:  DefaultUsedThreshold,
	}
	if !r2.ShouldFailoverToProviderPool() {
		t.Fatal("all Claude cooling: expect provider-pool failover")
	}

	// 2 profiles, one resets → false + EnsureUsableActive switches back
	r2.cooldown["b"] = time.Time{}
	if r2.ShouldFailoverToProviderPool() {
		t.Fatal("one Claude available: no failover")
	}
	r2.idx = 0 // active is still "a" (cooling)
	if !r2.EnsureUsableActive() {
		t.Fatal("EnsureUsableActive should pick b")
	}
	if r2.Active() != "b" {
		t.Fatalf("active=%q, want b", r2.Active())
	}
}
