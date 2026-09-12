package provider

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"amux-accounts/pkg/types"
)

func TestProvider_ResolveSecret(t *testing.T) {
	os.Setenv("TEST_AI_KEY", "secret-123")
	defer os.Unsetenv("TEST_AI_KEY")

	if got := ResolveSecret("env:TEST_AI_KEY"); got != "secret-123" {
		t.Errorf("expected secret-123, got %s", got)
	}
	if got := ResolveSecret("literal-key"); got != "literal-key" {
		t.Errorf("expected literal-key, got %s", got)
	}
}

func TestProvider_ConfigCRUD(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "am-provider-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	cfgPath := filepath.Join(tmpDir, "accounts.json")

	p1 := ProviderConfig{
		ID:       "groq-test",
		Type:     "openai_compatible",
		Priority: 1,
		BaseURL:  "https://api.groq.com/openai/v1",
		APIKey:   "key1",
		Model:    "llama-3.3-70b-versatile",
	}

	if err := AddOrUpdateProvider(cfgPath, p1); err != nil {
		t.Fatalf("AddOrUpdateProvider failed: %v", err)
	}

	file, err := LoadConfigFile(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfigFile failed: %v", err)
	}
	if len(file.Providers) != 1 || file.Providers[0].ID != "groq-test" {
		t.Fatalf("expected 1 provider groq-test, got %+v", file.Providers)
	}

	if err := RemoveProvider(cfgPath, "groq-test"); err != nil {
		t.Fatalf("RemoveProvider failed: %v", err)
	}
	file, _ = LoadConfigFile(cfgPath)
	if len(file.Providers) != 0 {
		t.Fatalf("expected 0 providers, got %d", len(file.Providers))
	}
}

func TestAddOrUpdateProvider_DuplicateAPIKeySkipped(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "accounts.json")

	p1 := ProviderConfig{
		ID: "geminiapi:01", Type: "gemini", Priority: 1,
		APIKey: "sk-same-key", Model: "gemini-3.6-flash",
	}
	if err := AddOrUpdateProvider(cfgPath, p1); err != nil {
		t.Fatalf("first add: %v", err)
	}

	// Same key, new id → skip
	err := AddOrUpdateProvider(cfgPath, ProviderConfig{
		ID: "geminiapi:02", Type: "gemini", Priority: 2,
		APIKey: "sk-same-key", Model: "gemini-3.6-flash",
	})
	dup, ok := err.(*DuplicateAPIKeyError)
	if !ok || dup.ExistingID != "geminiapi:01" {
		t.Fatalf("expected DuplicateAPIKeyError geminiapi:01, got %v", err)
	}
	file, _ := LoadConfigFile(cfgPath)
	if len(file.Providers) != 1 {
		t.Fatalf("expected still 1 provider, got %d", len(file.Providers))
	}

	// Same id + same key → update in place OK
	if err := AddOrUpdateProvider(cfgPath, ProviderConfig{
		ID: "geminiapi:01", Type: "gemini", Priority: 9,
		APIKey: "sk-same-key", Model: "gemini-2.5-pro",
	}); err != nil {
		t.Fatalf("self update: %v", err)
	}

	// env: ref resolving to same secret also blocked
	os.Setenv("AM_TEST_DUP_KEY", "sk-same-key")
	defer os.Unsetenv("AM_TEST_DUP_KEY")
	err = AddOrUpdateProvider(cfgPath, ProviderConfig{
		ID: "openrouter", Type: "openai_compatible", Priority: 3,
		BaseURL: "https://openrouter.ai/api/v1/",
		APIKey:  "env:AM_TEST_DUP_KEY",
		Model:   "x",
	})
	if _, ok := err.(*DuplicateAPIKeyError); !ok {
		t.Fatalf("expected DuplicateAPIKeyError for env ref, got %v", err)
	}

	// Different key OK
	if err := AddOrUpdateProvider(cfgPath, ProviderConfig{
		ID: "geminiapi:02", Type: "gemini", Priority: 2,
		APIKey: "sk-other-key", Model: "gemini-3.6-flash",
	}); err != nil {
		t.Fatalf("different key: %v", err)
	}
}

func TestDeduplicateProvidersByCredential_SessionKey(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "accounts.json")
	_ = AddOrUpdateProvider(cfgPath, ProviderConfig{
		ID: "claudeweb:01", Type: "claude_web", Priority: 1, SessionKey: "sk-ant-dup",
	})
	// Bypass AddOrUpdate to simulate legacy duplicates already on disk.
	f, _ := LoadConfigFile(cfgPath)
	f.Providers = append(f.Providers, ProviderConfig{
		ID: "claudeweb:02", Type: "claude_web", Priority: 2, SessionKey: "sk-ant-dup",
	})
	_ = SaveConfigFile(cfgPath, f)

	removed, err := DeduplicateProvidersByCredential(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0] != "claudeweb:02" {
		t.Fatalf("removed=%v", removed)
	}
	f, _ = LoadConfigFile(cfgPath)
	if len(f.Providers) != 1 || f.Providers[0].ID != "claudeweb:01" {
		t.Fatalf("providers=%+v", f.Providers)
	}
}

func TestBuildConcatenatedPrompt(t *testing.T) {
	// 1. Empty messages
	if got := BuildConcatenatedPrompt(nil); got != "" {
		t.Errorf("expected empty string for nil messages, got %q", got)
	}

	// 2. Single user message
	single := []types.ChatMessage{{Role: "user", Content: "Hello world"}}
	if got := BuildConcatenatedPrompt(single); got != "Hello world" {
		t.Errorf("expected plain 'Hello world', got %q", got)
	}

	// 3. Multi-turn conversation with system instructions
	multi := []types.ChatMessage{
		{Role: "system", Content: "Be helpful"},
		{Role: "user", Content: "Hi"},
		{Role: "assistant", Content: "Hello!"},
		{Role: "user", Content: "How are you?"},
	}
	got := BuildConcatenatedPrompt(multi)
	if !strings.Contains(got, "[System Instructions]\nBe helpful") {
		t.Errorf("expected system instructions, got %q", got)
	}
	if !strings.Contains(got, "User: Hi") || !strings.Contains(got, "Assistant: Hello!") {
		t.Errorf("expected history turns, got %q", got)
	}
	if !strings.Contains(got, "User: How are you?") {
		t.Errorf("expected final user prompt, got %q", got)
	}
	if !strings.HasSuffix(got, "Assistant:") {
		t.Errorf("expected suffix 'Assistant:', got %q", got)
	}
}

// TestBuildConcatenatedPrompt_SystemOnly locks in the behavior for the edge
// case flagged in code review: a request with only system message(s) and no
// user/assistant turns. It must not lose the system content, and it must
// not panic or produce something the web adapters would send as empty.
func TestBuildConcatenatedPrompt_SystemOnly(t *testing.T) {
	msgs := []types.ChatMessage{{Role: "system", Content: "Be terse."}}
	got := BuildConcatenatedPrompt(msgs)
	if !strings.Contains(got, "[System Instructions]\nBe terse.") {
		t.Errorf("expected system instructions preserved, got %q", got)
	}
	if !strings.HasSuffix(got, "Assistant:") {
		t.Errorf("expected trailing 'Assistant:' cue even with no history, got %q", got)
	}
	if got == "" {
		t.Errorf("system-only input must not collapse to an empty prompt")
	}
}

// TestBuildConcatenatedPrompt_MultipleSystemMessages verifies that several
// system messages are merged into one coherent block (fix for the original
// bug where the "[System Instructions]" header was repeated per message).
func TestBuildConcatenatedPrompt_MultipleSystemMessages(t *testing.T) {
	msgs := []types.ChatMessage{
		{Role: "system", Content: "Be helpful."},
		{Role: "system", Content: "Be concise."},
		{Role: "user", Content: "Hi"},
	}
	got := BuildConcatenatedPrompt(msgs)
	if strings.Count(got, "[System Instructions]") != 1 {
		t.Errorf("expected exactly one '[System Instructions]' header, got %q", got)
	}
	if !strings.Contains(got, "Be helpful.") || !strings.Contains(got, "Be concise.") {
		t.Errorf("expected both system messages preserved, got %q", got)
	}
}

func TestBuildAdapter_DefaultsAndMissing(t *testing.T) {
	// Missing baseUrl for openai_compatible
	_, err := BuildAdapter(ProviderConfig{Type: "openai_compatible"})
	if err == nil {
		t.Errorf("expected error for missing baseUrl")
	}

	// Defaults for openai_compatible
	a1, err := BuildAdapter(ProviderConfig{Type: "openai_compatible", BaseURL: "https://api.test.com"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	oa := a1.(*OpenAICompatibleAdapter)
	if oa.TargetModel != "gpt-4o" {
		t.Errorf("expected default model gpt-4o, got %s", oa.TargetModel)
	}

	if _, err := BuildAdapter(ProviderConfig{Type: "duckduckgo"}); err == nil {
		t.Fatal("expected duckduckgo type to be unsupported")
	}
}

func TestProvider_MigrateLegacyIDs(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "am-provider-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)
	cfgPath := filepath.Join(tmpDir, "accounts.json")

	f := &AccountsFile{Providers: []ProviderConfig{
		{ID: "claude-web", Type: "claude_web", Priority: 6},
		{ID: "chatgpt-web", Type: "chatgpt_web", Priority: 5},
		{ID: "my-custom-provider", Type: "openai_compatible", Priority: 10},
	}}
	if err := SaveConfigFile(cfgPath, f); err != nil {
		t.Fatalf("SaveConfigFile failed: %v", err)
	}

	if err := MigrateLegacyIDs(cfgPath); err != nil {
		t.Fatalf("MigrateLegacyIDs failed: %v", err)
	}

	got, err := LoadConfigFile(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfigFile failed: %v", err)
	}
	want := map[string]string{
		"claude_web":        "claude:web:01",
		"chatgpt_web":       "chatgpt:01",
		"openai_compatible": "my-custom-provider", // untouched: not a legacy literal ID
	}
	for _, p := range got.Providers {
		if p.ID != want[p.Type] {
			t.Errorf("provider type %q: ID = %q, want %q", p.Type, p.ID, want[p.Type])
		}
	}

	// Idempotent: running again on already-migrated IDs changes nothing.
	if err := MigrateLegacyIDs(cfgPath); err != nil {
		t.Fatalf("second MigrateLegacyIDs failed: %v", err)
	}
	again, err := LoadConfigFile(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfigFile (2nd) failed: %v", err)
	}
	for i, p := range again.Providers {
		if p.ID != got.Providers[i].ID {
			t.Errorf("second migration changed ID: %q -> %q", got.Providers[i].ID, p.ID)
		}
	}
}

func TestProvider_MigrateCompactIDs(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "accounts.json")
	f := &AccountsFile{Providers: []ProviderConfig{
		{ID: "geminiapi:01", Type: "gemini", Priority: 1},
		{ID: "claudeweb:01", Type: "claude_web", Priority: 2},
		{ID: "openrouter", Type: "openai_compatible", Priority: 3},
	}}
	if err := SaveConfigFile(cfgPath, f); err != nil {
		t.Fatal(err)
	}
	if err := MigrateLegacyIDs(cfgPath); err != nil {
		t.Fatal(err)
	}
	got, _ := LoadConfigFile(cfgPath)
	want := map[string]string{
		"gemini":            "gemini:api:01",
		"claude_web":        "claude:web:01",
		"openai_compatible": "openrouter:api:01",
	}
	for _, p := range got.Providers {
		if p.ID != want[p.Type] {
			t.Errorf("%s: got %q want %q", p.Type, p.ID, want[p.Type])
		}
	}
}

func TestProvider_SetPriority(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "am-provider-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)
	cfgPath := filepath.Join(tmpDir, "accounts.json")

	if err := AddOrUpdateProvider(cfgPath, ProviderConfig{ID: "claudeweb:01", Type: "claude_web", Priority: 6}); err != nil {
		t.Fatalf("AddOrUpdateProvider failed: %v", err)
	}

	// Existing ID: updates in place.
	if err := SetPriority(cfgPath, "claudeweb:01", 9); err != nil {
		t.Fatalf("SetPriority (existing) failed: %v", err)
	}
	f, _ := LoadConfigFile(cfgPath)
	if len(f.Providers) != 1 || f.Providers[0].Priority != 9 {
		t.Fatalf("expected priority 9, got %+v", f.Providers)
	}

	// codexcli-shaped ID with no existing row: inserted as a bare placeholder.
	if err := SetPriority(cfgPath, "codexcli:01", 4); err != nil {
		t.Fatalf("SetPriority (codex placeholder) failed: %v", err)
	}
	f, _ = LoadConfigFile(cfgPath)
	if len(f.Providers) != 2 {
		t.Fatalf("expected 2 providers after codex placeholder insert, got %d", len(f.Providers))
	}
	found := false
	for _, p := range f.Providers {
		if p.ID == "codexcli:01" {
			found = true
			if p.Type != "codex_cli" || p.Priority != 4 {
				t.Errorf("codex placeholder row = %+v, want type=codex_cli priority=4", p)
			}
		}
	}
	if !found {
		t.Fatalf("expected a codexcli:01 row to be inserted, got %+v", f.Providers)
	}

	// Unknown, non-codex ID: errors instead of silently inserting.
	if err := SetPriority(cfgPath, "does-not-exist", 1); err == nil {
		t.Errorf("expected error for unknown non-codex id")
	}
}

func TestProvider_SetModel(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "am-provider-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)
	cfgPath := filepath.Join(tmpDir, "accounts.json")

	if err := AddOrUpdateProvider(cfgPath, ProviderConfig{ID: "gemini:01", Type: "gemini", Priority: 2, Model: "gemini-2.0-flash"}); err != nil {
		t.Fatalf("AddOrUpdateProvider failed: %v", err)
	}

	if err := SetModel(cfgPath, "gemini:01", "gemini-1.5-pro"); err != nil {
		t.Fatalf("SetModel (existing) failed: %v", err)
	}
	f, _ := LoadConfigFile(cfgPath)
	if len(f.Providers) != 1 || f.Providers[0].Model != "gemini-1.5-pro" {
		t.Fatalf("expected model gemini-1.5-pro, got %+v", f.Providers)
	}

	if err := SetModel(cfgPath, "does-not-exist", "some-model"); err == nil {
		t.Errorf("expected error for unknown id")
	}
}

func TestProvider_IsConfigured(t *testing.T) {
	// Unconfigured openai_compatible (missing or unset env)
	p1 := ProviderConfig{Type: "openai_compatible", APIKey: "env:UNSET_KEY_9999"}
	if p1.IsConfigured() {
		t.Errorf("expected unset env key to be unconfigured")
	}

	// Configured openai_compatible
	p2 := ProviderConfig{Type: "openai_compatible", APIKey: "sk-real-key"}
	if !p2.IsConfigured() {
		t.Errorf("expected literal key to be configured")
	}

	// Unconfigured chatgpt_web
	p3 := ProviderConfig{Type: "chatgpt_web", SessionToken: "env:UNSET_TOK_9999"}
	if p3.IsConfigured() {
		t.Errorf("expected unset session token to be unconfigured")
	}

	// Explicitly disabled openai_compatible
	f := false
	p4 := ProviderConfig{Type: "openai_compatible", APIKey: "sk-x", Enabled: &f}
	if p4.IsConfigured() {
		t.Errorf("expected disabled provider to be unconfigured")
	}
}
