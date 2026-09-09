package profile

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"ai-cli-accounts/pkg/types"
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
