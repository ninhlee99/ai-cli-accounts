package provider

import (
	"path/filepath"
	"testing"
)

func TestMatchID_PrefixAndExact(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "accounts.json")
	f := &AccountsFile{Providers: []ProviderConfig{
		{ID: "gemini:api:01", Type: "gemini"},
		{ID: "gemini:web:01", Type: "gemini_web"},
		{ID: "chatgpt:01", Type: "chatgpt_web"},
	}}
	if err := SaveConfigFile(path, f); err != nil {
		t.Fatal(err)
	}
	got, err := MatchID(path, "chatgpt:01")
	if err != nil || got != "chatgpt:01" {
		t.Fatalf("exact: %q %v", got, err)
	}
	got, err = MatchID(path, "gemini:web")
	if err != nil || got != "gemini:web:01" {
		t.Fatalf("prefix: %q %v", got, err)
	}
	if _, err := MatchID(path, "gemini"); err == nil {
		t.Fatal("expected ambiguous gemini")
	}
	if _, err := MatchID(path, "nope"); err == nil {
		t.Fatal("expected missing")
	}
}
