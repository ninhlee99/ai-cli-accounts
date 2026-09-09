package bridge_test

import (
	"testing"

	"ai-cli-accounts/pkg/bridge"
)

func TestToChatRequest_SystemAndMessages(t *testing.T) {
	rawJSON := []byte(`{
		"model": "claude-3-5-sonnet-20241022",
		"system": "You are a senior golang engineer.",
		"messages": [
			{"role": "user", "content": "Explain channels in Go"},
			{"role": "assistant", "content": "Channels are concurrency primitives"},
			{"role": "user", "content": [{"type": "text", "text": "Give an example"}]}
		],
		"stream": true,
		"max_tokens": 1024
	}`)

	req, err := bridge.ToChatRequest(rawJSON)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if req.Model != "claude-3-5-sonnet-20241022" {
		t.Errorf("expected model claude-3-5-sonnet-20241022, got %s", req.Model)
	}
	if !req.Stream {
		t.Errorf("expected stream=true")
	}
	if len(req.Messages) != 4 {
		t.Fatalf("expected 4 messages (1 system + 3 turns), got %d", len(req.Messages))
	}
	if req.Messages[0].Role != "system" || req.Messages[0].Content != "You are a senior golang engineer." {
		t.Errorf("unexpected system message: %+v", req.Messages[0])
	}
	if req.Messages[3].Role != "user" || req.Messages[3].Content != "Give an example" {
		t.Errorf("unexpected block message: %+v", req.Messages[3])
	}
}
