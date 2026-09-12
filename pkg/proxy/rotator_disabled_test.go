package proxy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"amux-accounts/pkg/profile"
	"amux-accounts/pkg/types"
)

func TestForceSwitch_RejectsDisabled(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AM_HOME", dir)
	tool := "claude"
	_ = os.MkdirAll(profile.ProfileDir(tool), 0o700)
	for _, name := range []string{"a", "b"} {
		meta := types.ProfileMeta{Name: name, Tool: tool, Account: name + "@x.com", Saved: time.Now()}
		b, _ := json.MarshalIndent(meta, "", "  ")
		_ = os.WriteFile(filepath.Join(profile.ProfileDir(tool), name+".meta.json"), b, 0o600)
		_ = os.WriteFile(filepath.Join(profile.ProfileDir(tool), name+".amp"), []byte("{}"), 0o600)
	}
	if err := profile.SetDisabled(tool, "b", true); err != nil {
		t.Fatal(err)
	}

	r := NewRotator(tool)
	if err := r.ForceSwitch("b"); err == nil {
		t.Fatal("expected reject disabled profile")
	}
	if !r.AllUnavailable() && len(r.Names()) == 2 {
		// a is still available unless marked dead — AllUnavailable should be false
	}
	// with only b disabled, a usable → not all unavailable
	if r.AllUnavailable() {
		t.Fatal("a should still be available")
	}
}

func TestRotate_SkipsDisabled(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AM_HOME", dir)
	tool := "claude"
	_ = os.MkdirAll(profile.ProfileDir(tool), 0o700)
	for _, name := range []string{"a", "b", "c"} {
		meta := types.ProfileMeta{Name: name, Tool: tool, Account: name + "@x.com", Saved: time.Now()}
		b, _ := json.MarshalIndent(meta, "", "  ")
		_ = os.WriteFile(filepath.Join(profile.ProfileDir(tool), name+".meta.json"), b, 0o600)
		_ = os.WriteFile(filepath.Join(profile.ProfileDir(tool), name+".amp"), []byte("{}"), 0o600)
	}
	_ = profile.SetDisabled(tool, "b", true)

	r := NewRotator(tool)
	_ = r.ForceSwitch("a")
	// InstallActiveProfile will fail without real tokens — Rotate marks dead.
	// Still verify disabled candidate is never chosen: mark a cooling and
	// ensure we don't land on b.
	r.mu.Lock()
	r.cooldown["a"] = time.Now().Add(time.Hour)
	r.mu.Unlock()
	r.Rotate("a", "test")
	if r.Active() == "b" {
		t.Fatal("rotate must not select disabled b")
	}
}

// TestToken_DisabledSoleProfile reproduces the reported bug: a user turns
// off their only Claude subscription profile, expecting it to never be used
// again — even if no other Claude profile exists to fail over to. Token()
// must return "" rather than silently falling back to the disabled
// profile's live keychain credentials, and ProfileCount() must not count it
// as an available option (so callers correctly treat Claude as unusable and
// fail over to the provider pool, or fail cleanly, instead of leaking a
// request through the off subscription).
func TestToken_DisabledSoleProfile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AM_HOME", dir)
	tool := "claude"
	_ = os.MkdirAll(profile.ProfileDir(tool), 0o700)
	name := "only"
	meta := types.ProfileMeta{Name: name, Tool: tool, Account: name + "@x.com", Saved: time.Now()}
	b, _ := json.MarshalIndent(meta, "", "  ")
	_ = os.WriteFile(filepath.Join(profile.ProfileDir(tool), name+".meta.json"), b, 0o600)
	_ = os.WriteFile(filepath.Join(profile.ProfileDir(tool), name+".amp"), []byte("{}"), 0o600)
	_ = profile.SetDisabled(tool, name, true)

	r := NewRotator(tool)
	if got := r.Token(); got != "" {
		t.Fatalf("Token() for disabled sole profile = %q, want empty", got)
	}
	if n := r.ProfileCount(); n != 0 {
		t.Fatalf("ProfileCount() = %d, want 0 (disabled profile must not count as usable)", n)
	}
	if err := r.ForceSwitchExplicit(name); err == nil {
		t.Fatal("ForceSwitchExplicit must reject a disabled profile — X-Provider is not a bypass for `am off`")
	}
}
