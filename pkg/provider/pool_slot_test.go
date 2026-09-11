package provider

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNamedPoolID(t *testing.T) {
	if got := NamedPoolID("claude_web", "ninhle@x.com"); got != "claude:web:ninhle" {
		t.Fatalf("got %q", got)
	}
	if got := NamedPoolID("chatgpt_web", "tungnt@y.com"); got != "chatgpt:web:tungnt" {
		t.Fatalf("got %q", got)
	}
	if got := NamedPoolID("openai_compatible", "a@b.com"); got != "" {
		t.Fatalf("expected empty for custom type, got %q", got)
	}
}

func TestResolvePoolSlot_NewNamed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "accounts.json")

	slot := ResolvePoolSlot(path, "claude_web", "ninhle@x.com")
	if slot.ID != "claude:web:ninhle" || slot.Relogin {
		t.Fatalf("slot = %+v", slot)
	}
	if slot.Priority != PriorityWebClaude {
		t.Fatalf("priority = %d, want %d", slot.Priority, PriorityWebClaude)
	}
}

func TestResolvePoolSlot_ReloginSameAccount(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "accounts.json")
	if err := AddOrUpdateProvider(path, ProviderConfig{
		ID:       "claude:web:ninhle",
		Type:     "claude_web",
		Priority: 25,
		Account:  "ninhle@x.com",
		SessionKey: "old-key",
	}); err != nil {
		t.Fatal(err)
	}

	slot := ResolvePoolSlot(path, "claude_web", "ninhle@x.com")
	if !slot.Relogin || slot.ID != "claude:web:ninhle" || slot.Priority != 25 {
		t.Fatalf("slot = %+v", slot)
	}
	if slot.RenameFrom != "" {
		t.Fatalf("unexpected rename %q", slot.RenameFrom)
	}
}

func TestResolvePoolSlot_ReloginCaseInsensitiveEmail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "accounts.json")
	_ = AddOrUpdateProvider(path, ProviderConfig{
		ID: "claude:web:ninhle", Type: "claude_web", Priority: 25, Account: "NinhLe@X.COM",
	})
	slot := ResolvePoolSlot(path, "claude_web", "ninhle@x.com")
	if !slot.Relogin || slot.ID != "claude:web:ninhle" {
		t.Fatalf("slot = %+v", slot)
	}
}

func TestResolvePoolSlot_PromoteAnonymous(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "accounts.json")
	_ = AddOrUpdateProvider(path, ProviderConfig{
		ID: "claudeweb:01", Type: "claude_web", Priority: 25, SessionKey: "old",
	})

	slot := ResolvePoolSlot(path, "claude_web", "ninhle@x.com")
	if !slot.Relogin || slot.ID != "claude:web:ninhle" || slot.RenameFrom != "claudeweb:01" {
		t.Fatalf("slot = %+v", slot)
	}
}

func TestResolvePoolSlot_SecondAccountGetsOwnID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "accounts.json")
	_ = AddOrUpdateProvider(path, ProviderConfig{
		ID: "claude:web:ninhle", Type: "claude_web", Priority: 25, Account: "ninhle@x.com",
	})

	slot := ResolvePoolSlot(path, "claude_web", "tungnt@y.com")
	if slot.Relogin || slot.ID != "claude:web:tungnt" {
		t.Fatalf("slot = %+v", slot)
	}
	if slot.Priority != 26 {
		t.Fatalf("priority = %d, want 26", slot.Priority)
	}
}

func TestResolvePoolSlot_SameLocalPartDifferentDomain(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "accounts.json")
	_ = AddOrUpdateProvider(path, ProviderConfig{
		ID: "claude:web:ninhle", Type: "claude_web", Priority: 25, Account: "ninhle@gmail.com",
	})

	slot := ResolvePoolSlot(path, "claude_web", "ninhle@company.io")
	if slot.Relogin {
		t.Fatalf("expected new account, got relogin: %+v", slot)
	}
	if slot.ID != "claude:web:ninhle-companyio" {
		t.Fatalf("slot.ID = %q, want claude:web:ninhle-companyio", slot.ID)
	}

	// Relogin of the domain-disambiguated account keeps that ID.
	_ = AddOrUpdateProvider(path, ProviderConfig{
		ID: slot.ID, Type: "claude_web", Priority: slot.Priority, Account: "ninhle@company.io",
	})
	again := ResolvePoolSlot(path, "claude_web", "ninhle@company.io")
	if !again.Relogin || again.ID != "claude:web:ninhle-companyio" {
		t.Fatalf("relogin slot = %+v", again)
	}

	// Original short ID still maps to the first email.
	first := ResolvePoolSlot(path, "claude_web", "ninhle@gmail.com")
	if !first.Relogin || first.ID != "claude:web:ninhle" {
		t.Fatalf("first slot = %+v", first)
	}
}

func TestUpsertPoolProvider_Rename(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "accounts.json")
	_ = AddOrUpdateProvider(path, ProviderConfig{
		ID: "claudeweb:01", Type: "claude_web", Priority: 25, SessionKey: "old",
	})

	err := UpsertPoolProvider(path, ProviderConfig{
		ID: "claude:web:ninhle", Type: "claude_web", Priority: 25,
		Account: "ninhle@x.com", SessionKey: "new",
	}, "claudeweb:01")
	if err != nil {
		t.Fatal(err)
	}
	f, err := LoadConfigFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Providers) != 1 {
		t.Fatalf("len = %d", len(f.Providers))
	}
	p := f.Providers[0]
	if p.ID != "claude:web:ninhle" || p.SessionKey != "new" || p.Account != "ninhle@x.com" {
		t.Fatalf("got %+v", p)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}
