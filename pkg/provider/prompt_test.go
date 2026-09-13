package provider

import (
	"strings"
	"testing"

	"amux-accounts/pkg/types"
)

func TestWebBackendPrompt_FullContextFlattensHistory(t *testing.T) {
	req := &types.ChatRequest{
		FullContext: true,
		Messages: []types.ChatMessage{
			{Role: "system", Content: "You are helpful."},
			{Role: "user", Content: "read foo.go"},
			{Role: "assistant", Content: "[Prior tool call: Read]\nfoo.go"},
			{Role: "user", Content: "fix the bug"},
		},
	}
	got := WebBackendPrompt(req, true) // continuingThread ignored when FullContext
	if !strings.Contains(got, "[xfer]") {
		t.Fatalf("missing handoff preamble: %q", got)
	}
	if !strings.Contains(got, "read foo.go") || !strings.Contains(got, "fix the bug") {
		t.Fatalf("missing turns: %q", got)
	}
	if !strings.Contains(got, "You are helpful.") {
		t.Fatalf("missing system: %q", got)
	}
	if strings.Contains(got, "If you cannot invoke tools") {
		t.Fatal("old handoff told the model to refuse tools")
	}
}

func TestWebBackendPrompt_ToolsCloserLast(t *testing.T) {
	req := &types.ChatRequest{
		FullContext: true,
		Tools:       []types.ToolDef{{Name: "Read"}, {Name: "Bash"}},
		Messages: []types.ChatMessage{
			{Role: "system", Content: "Tools run behind a user-selected permission mode."},
			{Role: "user", Content: "please audit the readme file now"},
		},
	}
	got := WebBackendPrompt(req, false)
	closer := "[end]"
	if !strings.Contains(got, closer) {
		t.Fatal("missing closer")
	}
	if strings.LastIndex(got, closer) < strings.LastIndex(got, "please audit the readme file now") {
		t.Fatal("closer must follow user text")
	}
	if strings.Contains(got, "permission mode") {
		t.Fatal("harness system must be stripped")
	}
	if !strings.Contains(got, "<tool_call>") {
		t.Fatal("missing protocol")
	}
}

func TestWebBackendPrompt_ContinuingUsesLastUser(t *testing.T) {
	req := &types.ChatRequest{
		Messages: []types.ChatMessage{
			{Role: "system", Content: "sys"},
			{Role: "user", Content: "first"},
			{Role: "assistant", Content: "ok"},
			{Role: "user", Content: "second"},
		},
	}
	got := WebBackendPrompt(req, true)
	if !strings.Contains(got, "sys") || !strings.Contains(got, "second") {
		t.Fatalf("got %q", got)
	}
	if strings.Contains(got, "first") {
		t.Fatalf("should not flatten when continuing: %q", got)
	}
}

func TestWebBackendPrompt_LiveCatalogNoCodeChange(t *testing.T) {
	req := &types.ChatRequest{
		FullContext: true,
		Tools: []types.ToolDef{
			{Name: "mcp__new__search", InputSchema: []byte(`{"required":["q"],"properties":{"q":{"type":"string"}}}`)},
			{Name: "Skill", InputSchema: []byte(`{"required":["skill"],"properties":{"skill":{"type":"string"}}}`)},
		},
		Messages: []types.ChatMessage{
			{Role: "system", Content: "You are Claude Code, Anthropic's official CLI for Claude.\n" + strings.Repeat("x", 80)},
			{Role: "user", Content: "use the new mcp"},
		},
	}
	got := WebBackendPrompt(req, false)
	if !strings.Contains(got, "mcp__new__search:q") || !strings.Contains(got, "Skill:skill") {
		t.Fatalf("live catalog: %s", got)
	}
	if strings.Contains(got, "You are Claude Code") {
		t.Fatal("harness leaked")
	}
}

func TestWebBackendPrompt_StripsSystemReminder(t *testing.T) {
	req := &types.ChatRequest{
		FullContext: true,
		Tools:       []types.ToolDef{{Name: "Read"}},
		Messages: []types.ChatMessage{
			{Role: "user", Content: "<system-reminder>\nYou are Claude Code\n" + strings.Repeat("skill ", 40) + "\n</system-reminder>\n\nreview README.md"},
		},
	}
	got := WebBackendPrompt(req, false)
	if strings.Contains(got, "system-reminder") || strings.Contains(got, "You are Claude Code") {
		t.Fatal("reminder leaked", got)
	}
	if !strings.Contains(got, "review README.md") {
		t.Fatal("user task dropped", got)
	}
}

func TestWebBackendPrompt_NoToolsOmitsProtocol(t *testing.T) {
	got := WebBackendPrompt(&types.ChatRequest{
		Messages: []types.ChatMessage{{Role: "user", Content: "hi"}},
	}, false)
	if strings.Contains(got, "<tool_call>") || strings.Contains(got, "CATALOG") {
		t.Fatal("no tools[] must not inject web protocol", got)
	}
}
