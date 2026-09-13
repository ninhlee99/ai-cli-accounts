package hook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestGeminiHookInstallUninstall(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	// Pre-existing hooks file with an unrelated hook
	configDir := filepath.Join(tmp, ".gemini", "config")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	initial := map[string]any{
		"my-custom-hook": map[string]any{
			"PreToolUse": []any{
				map[string]any{"type": "command", "command": "echo test"},
			},
		},
	}
	b, _ := json.MarshalIndent(initial, "", "  ")
	if err := os.WriteFile(filepath.Join(configDir, "hooks.json"), b, 0o600); err != nil {
		t.Fatalf("write initial hooks.json: %v", err)
	}

	// 1. Install Gemini hook
	if err := GeminiHookInstall(); err != nil {
		t.Fatalf("GeminiHookInstall failed: %v", err)
	}

	if !GeminiHookInstalled() {
		t.Fatalf("expected GeminiHookInstalled() to be true")
	}

	events := GeminiInstalledEvents()
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %v", events)
	}

	// Verify custom hook is still preserved
	m := LoadGeminiHooks()
	if _, ok := m["my-custom-hook"]; !ok {
		t.Fatalf("expected my-custom-hook to be preserved")
	}

	// 2. Uninstall Gemini hook
	n, err := GeminiHookUninstall()
	if err != nil {
		t.Fatalf("GeminiHookUninstall failed: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 hook removed, got %d", n)
	}

	if GeminiHookInstalled() {
		t.Fatalf("expected GeminiHookInstalled() to be false after uninstall")
	}

	// Verify custom hook is STILL preserved
	mAfter := LoadGeminiHooks()
	if _, ok := mAfter["my-custom-hook"]; !ok {
		t.Fatalf("expected my-custom-hook to still be preserved")
	}
}

func TestCodexHookInstallUninstall(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	_ = os.MkdirAll(filepath.Join(tmp, ".codex"), 0o755)

	if err := CodexHookInstall(); err != nil {
		t.Fatalf("CodexHookInstall failed: %v", err)
	}
	if !CodexHookInstalled() {
		t.Fatalf("expected CodexHookInstalled() to be true")
	}

	n, err := CodexHookUninstall()
	if err != nil {
		t.Fatalf("CodexHookUninstall failed: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 removed, got %d", n)
	}
	if CodexHookInstalled() {
		t.Fatalf("expected CodexHookInstalled() to be false")
	}
}

func TestCursorHookInstallUninstall(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	_ = os.MkdirAll(filepath.Join(tmp, ".cursor"), 0o755)

	if err := CursorHookInstall(); err != nil {
		t.Fatalf("CursorHookInstall failed: %v", err)
	}
	if !CursorHookInstalled() {
		t.Fatalf("expected CursorHookInstalled() to be true")
	}

	n, err := CursorHookUninstall()
	if err != nil {
		t.Fatalf("CursorHookUninstall failed: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 removed, got %d", n)
	}
	if CursorHookInstalled() {
		t.Fatalf("expected CursorHookInstalled() to be false")
	}
}

