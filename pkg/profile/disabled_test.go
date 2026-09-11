package profile

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"amux-accounts/pkg/types"
)

func TestSetDisabled_PersistsAndClears(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AM_HOME", dir)
	tool := "claude"
	name := "acct1"
	_ = os.MkdirAll(ProfileDir(tool), 0o700)
	meta := types.ProfileMeta{Name: name, Tool: tool, Account: "a@b.c", Saved: time.Now()}
	b, _ := json.MarshalIndent(meta, "", "  ")
	if err := os.WriteFile(MetaPath(tool, name), b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(BundlePath(tool, name), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := SetDisabled(tool, name, true); err != nil {
		t.Fatal(err)
	}
	if !IsDisabled(tool, name) {
		t.Fatal("expected disabled")
	}
	raw, _ := os.ReadFile(MetaPath(tool, name))
	if !strings.Contains(string(raw), `"disabled": true`) && !strings.Contains(string(raw), `"disabled":true`) {
		t.Fatalf("meta missing disabled: %s", raw)
	}
	if err := SetDisabled(tool, name, false); err != nil {
		t.Fatal(err)
	}
	if IsDisabled(tool, name) {
		t.Fatal("expected enabled")
	}
}
