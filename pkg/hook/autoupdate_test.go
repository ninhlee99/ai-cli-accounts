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

	if IsAutoUpdateEnabled() {
		t.Errorf("expected auto update to be disabled initially")
	}

	cfgFile := AutoUpdateConfigFile()
	if filepath.Dir(cfgFile) != tmp {
		t.Errorf("expected config in %s, got %s", tmp, cfgFile)
	}
}
