package hook

import (
	"path/filepath"
	"testing"
)

func TestLaunchAgentPath(t *testing.T) {
	p := LaunchAgentPath()
	if filepath.Base(p) != AutoUpdatePlistLabel+".plist" {
		t.Errorf("unexpected plist name: %s", p)
	}
}

func TestAutoUpdateConfig(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("AM_DIR", tmp)

	// IsAutoUpdateEnabled falls back to checking the real LaunchAgent
	// plist when AutoUpdateConfigFile() (scoped to AM_DIR above) doesn't
	// exist yet — deliberately, since older installs may have set up the
	// LaunchAgent before this config file existed. That fallback path is
	// NOT scoped to AM_DIR (launchd requires the real path), so on a
	// machine that genuinely has auto-update enabled for real, it would
	// otherwise leak into this "fresh install" assertion. Scope it to the
	// temp dir too, just for this test.
	oldOverride := launchAgentPathOverride
	launchAgentPathOverride = filepath.Join(tmp, "LaunchAgents", AutoUpdatePlistLabel+".plist")
	t.Cleanup(func() { launchAgentPathOverride = oldOverride })

	if IsAutoUpdateEnabled() {
		t.Errorf("expected auto update to be disabled initially")
	}

	cfgFile := AutoUpdateConfigFile()
	if filepath.Dir(cfgFile) != tmp {
		t.Errorf("expected config in %s, got %s", tmp, cfgFile)
	}
}
