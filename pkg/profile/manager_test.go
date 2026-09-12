package profile

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"amux-accounts/pkg/types"
)

func TestProfile_SanitizeName(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"user@gmail.com", "user@gmail.com"},
		{"work/personal", "work-personal"},
		{"team dev", "team-dev"},
	}
	for _, tc := range cases {
		got := SanitizeName(tc.in)
		if got != tc.want {
			t.Errorf("SanitizeName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestProfile_BundleRoundtrip(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "am-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	oldDir := os.Getenv("AM_DIR")
	os.Setenv("AM_DIR", tmpDir)
	defer os.Setenv("AM_DIR", oldDir)

	tool := "claude"
	name := "testprofile"

	testData := []byte("fake-claude-credential-data")
	entries := []types.ProfileEntry{
		{
			Artifact: types.Artifact{
				Kind:    "file",
				Path:    filepath.Join(tmpDir, "dummy.json"),
				AccountField: "email",
			},
			Data: testData,
		},
	}

	if err := WriteBundle(tool, name, entries); err != nil {
		t.Fatalf("WriteBundle failed: %v", err)
	}

	loaded := LoadProfileEntries(tool, name)
	if len(loaded) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(loaded))
	}
	if !bytes.Equal(loaded[0].Data, testData) {
		t.Fatalf("loaded data mismatch: got %s, want %s", string(loaded[0].Data), string(testData))
	}
}

func TestProfile_IDPrefixForTool(t *testing.T) {
	cases := map[string]string{
		"claude":      "claude:code",
		"codex":       "codex",
		"gemini":      "gemini:web",
		"antigravity": "antigravity",
		"sometool":    "sometoolcli", // unmapped tool falls back to "<tool>cli"
	}
	for tool, want := range cases {
		if got := IDPrefixForTool(tool); got != want {
			t.Errorf("IDPrefixForTool(%q) = %q, want %q", tool, got, want)
		}
	}
}

func TestProfile_ListProfilesUnifiedIDs(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "am-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	oldDir := os.Getenv("AM_DIR")
	os.Setenv("AM_DIR", tmpDir)
	defer os.Setenv("AM_DIR", oldDir)

	tool := "codex"
	if err := os.MkdirAll(ProfileDir(tool), 0o700); err != nil {
		t.Fatal(err)
	}
	for i, name := range []string{"alpha", "beta"} {
		meta := types.ProfileMeta{
			Name:    name,
			Tool:    tool,
			Account: name + "@example.com",
			Saved:   time.Now().Add(time.Duration(i) * time.Second),
		}
		b, err := json.Marshal(meta)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(MetaPath(tool, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	got := ListProfiles(tool)
	if len(got) != 2 {
		t.Fatalf("expected 2 profiles, got %d: %+v", len(got), got)
	}
	want := []string{"codex:01", "codex:02"}
	for i, m := range got {
		if m.ID != want[i] {
			t.Errorf("profile %d (%s): ID = %q, want %q", i, m.Name, m.ID, want[i])
		}
	}

	// antigravity is the new placeholder tool: no artifacts/detection logic
	// yet, so listing it with nothing saved returns zero profiles rather
	// than erroring.
	if got := ListProfiles("antigravity"); len(got) != 0 {
		t.Errorf("expected 0 antigravity profiles, got %d: %+v", len(got), got)
	}
}

func TestProfile_TransferRoundtrip(t *testing.T) {
	pass := []byte("secret123")
	bundle := PortableBundle{
		Version:  1,
		Exported: time.Now(),
		Profiles: []PortableProfile{
			{
				Tool:    "claude",
				Name:    "work",
				Account: "work@example.com",
				Saved:   time.Now(),
				Entries: []types.ProfileEntry{
					{
						Artifact: types.Artifact{Kind: "file", Path: "/tmp/foo"},
						Data:     []byte("test data"),
					},
				},
			},
		},
	}

	// 1. Passphrase-based export
	sealed, err := SealBundle(bundle, pass, true)
	if err != nil {
		t.Fatalf("SealBundle failed: %v", err)
	}

	opened, err := OpenBundle(sealed, pass, true)
	if err != nil {
		t.Fatalf("OpenBundle failed: %v", err)
	}
	if len(opened.Profiles) != 1 || opened.Profiles[0].Name != "work" {
		t.Fatalf("opened mismatch: %+v", opened)
	}

	// 2. MasterKey-based local backup
	mKey := []byte("01234567890123456789012345678901")
	sealedBackup, err := SealBundle(bundle, mKey, false)
	if err != nil {
		t.Fatalf("SealBundle backup failed: %v", err)
	}

	openedBackup, err := OpenBundle(sealedBackup, mKey, false)
	if err != nil {
		t.Fatalf("OpenBundle backup failed: %v", err)
	}
	if len(openedBackup.Profiles) != 1 || openedBackup.Profiles[0].Account != "work@example.com" {
		t.Fatalf("openedBackup mismatch: %+v", openedBackup)
	}
}
