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
	if !strings.Contains(got, "Context transfer") {
		t.Fatalf("missing handoff preamble: %q", got)
	}
	if !strings.Contains(got, "read foo.go") || !strings.Contains(got, "fix the bug") {
		t.Fatalf("missing turns: %q", got)
	}
	if !strings.Contains(got, "You are helpful.") {
		t.Fatalf("missing system: %q", got)
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
