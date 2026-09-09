package provider

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ai-cli-accounts/pkg/types"
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

	// Defaults for duckduckgo
	a2, err := BuildAdapter(ProviderConfig{Type: "duckduckgo"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ddg := a2.(*DuckDuckGoAdapter)
	if ddg.TargetModel != "claude-3-haiku-20240307" {
		t.Errorf("expected default duckduckgo model, got %s", ddg.TargetModel)
	}
}
