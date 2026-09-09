package cli

import (
	"testing"
)

func TestToolAndName(t *testing.T) {
	tests := []struct {
		input    []string
		wantTool string
		wantName string
	}{
		{[]string{}, "claude", ""},
		{[]string{"work"}, "claude", "work"},
		{[]string{"user@gmail.com"}, "claude", "user@gmail.com"},
		{[]string{"codex", "personal"}, "codex", "personal"},
		{[]string{"gemini", "my-key"}, "gemini", "my-key"},
		{[]string{"claude1"}, "claude", "claude1"}, // not a unified ID, just a plain name -> default tool
		{[]string{"codexcli:01"}, "codex", "codexcli:01"},
		{[]string{"geminicli:02"}, "antigravity", "geminicli:02"},
	}

	for _, tt := range tests {
		tool, name := toolAndName(tt.input)
		if tool != tt.wantTool || name != tt.wantName {
			t.Errorf("toolAndName(%v) = (%q, %q), want (%q, %q)",
				tt.input, tool, name, tt.wantTool, tt.wantName)
		}
	}
}

func TestOrDash(t *testing.T) {
	if orDash("") != "-" {
		t.Errorf("orDash(\"\") = %q, want \"-\"", orDash(""))
	}
	if orDash("hello") != "hello" {
		t.Errorf("orDash(\"hello\") = %q, want \"hello\"", orDash("hello"))
	}
}

func TestProfileName(t *testing.T) {
	if profileName("my work", "test@domain.com") != "my-work" {
		t.Errorf("profileName with custom name failed")
	}
	if profileName("", "test@domain.com") != "test@domain.com" {
		t.Errorf("profileName with empty name should fallback to account")
	}
}
