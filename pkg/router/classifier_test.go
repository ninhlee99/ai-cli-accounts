package router

import (
	"testing"

	"amux-accounts/pkg/types"
)

func TestClassifyTask(t *testing.T) {
	cases := []struct {
		name          string
		req           *types.ChatRequest
		wantHeavy     bool
		wantThinking  bool
		wantEffort    string
	}{
		{
			name: "trivial prompt",
			req: &types.ChatRequest{
				Messages: []types.ChatMessage{
					{Role: "user", Content: "hello"},
				},
			},
			wantHeavy:    false,
			wantThinking: false,
		},
		{
			name: "deep architecture analysis in Vietnamese",
			req: &types.ChatRequest{
				Messages: []types.ChatMessage{
					{Role: "user", Content: "Bạn hãy phân tích chuyên sâu kiến trúc hệ thống multi-agent và tối ưu hiệu năng."},
				},
			},
			wantHeavy:    true,
			wantThinking: true,
			wantEffort:   "high",
		},
		{
			name: "security audit task",
			req: &types.ChatRequest{
				Messages: []types.ChatMessage{
					{Role: "user", Content: "Please perform a security audit on this authentication handler for potential vulnerability."},
				},
			},
			wantHeavy:    true,
			wantThinking: true,
			wantEffort:   "high",
		},
		{
			name: "stack trace crash investigation",
			req: &types.ChatRequest{
				Messages: []types.ChatMessage{
					{Role: "user", Content: "Investigate this crash:\npanic: runtime error: invalid memory address or nil pointer dereference\n[signal SIGSEGV]"},
				},
			},
			wantHeavy:    true,
			wantThinking: true,
			wantEffort:   "high",
		},
		{
			name: "explicit client thinking request",
			req: &types.ChatRequest{
				Thinking: true,
				Messages: []types.ChatMessage{
					{Role: "user", Content: "Count numbers from 1 to 5"},
				},
			},
			wantHeavy:    false,
			wantThinking: true,
			wantEffort:   "medium",
		},
		{
			name: "explicit pro model request",
			req: &types.ChatRequest{
				Model: "gemini-3.1-pro",
				Messages: []types.ChatMessage{
					{Role: "user", Content: "Quick question"},
				},
			},
			wantHeavy:    true,
			wantThinking: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := ClassifyTask(tc.req)
			if res.IsHeavy != tc.wantHeavy {
				t.Errorf("IsHeavy = %v, want %v (reasons: %v)", res.IsHeavy, tc.wantHeavy, res.Reasons)
			}
			if res.NeedsThinking != tc.wantThinking {
				t.Errorf("NeedsThinking = %v, want %v (reasons: %v)", res.NeedsThinking, tc.wantThinking, res.Reasons)
			}
			if tc.wantEffort != "" && res.ReasoningEffort != tc.wantEffort {
				t.Errorf("ReasoningEffort = %q, want %q", res.ReasoningEffort, tc.wantEffort)
			}
		})
	}
}
