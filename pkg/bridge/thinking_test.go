package bridge_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"amux-accounts/pkg/bridge"
	"amux-accounts/pkg/router"
	"amux-accounts/pkg/types"
)

type thinkingMockAdapter struct {
	id       string
	thinking string
	content  string
}

func (m *thinkingMockAdapter) ID() string    { return m.id }
func (m *thinkingMockAdapter) Priority() int { return 1 }
func (m *thinkingMockAdapter) SendMessageStream(ctx context.Context, req *types.ChatRequest) (<-chan types.StreamChunk, error) {
	ch := make(chan types.StreamChunk, 4)
	if m.thinking != "" {
		ch <- types.StreamChunk{ID: m.id, Thinking: m.thinking}
	}
	if m.content != "" {
		ch <- types.StreamChunk{ID: m.id, Content: m.content}
	}
	ch <- types.StreamChunk{ID: m.id, Done: true}
	close(ch)
	return ch, nil
}

func TestClaudeBridge_ThinkingStreaming(t *testing.T) {
	mock := &thinkingMockAdapter{
		id:       "mock-thinking",
		thinking: "Deep architectural reasoning step...",
		content:  "Here is the final architecture plan.",
	}
	pool := router.NewAccountPoolRouter([]types.ProviderAdapter{mock})

	reqBody := []byte(`{
		"model": "claude-3-7-sonnet-20250219",
		"stream": true,
		"thinking": {
			"type": "enabled",
			"budget_tokens": 2048
		},
		"messages": [
			{"role": "user", "content": "Please analyze system architecture"}
		]
	}`)

	r := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(reqBody))
	w := httptest.NewRecorder()

	err := bridge.HandleClaudeMessages(w, r, pool, reqBody)
	if err != nil {
		t.Fatalf("HandleClaudeMessages: %v", err)
	}

	body := w.Body.String()
	if !strings.Contains(body, `"type":"thinking"`) {
		t.Errorf("expected thinking block start in SSE, got:\n%s", body)
	}
	if !strings.Contains(body, `"type":"thinking_delta"`) {
		t.Errorf("expected thinking_delta in SSE, got:\n%s", body)
	}
	if !strings.Contains(body, "Deep architectural reasoning step...") {
		t.Errorf("expected thinking content in SSE, got:\n%s", body)
	}
	if !strings.Contains(body, `"type":"text_delta"`) {
		t.Errorf("expected text_delta in SSE, got:\n%s", body)
	}
	if !strings.Contains(body, "Here is the final architecture plan.") {
		t.Errorf("expected final text in SSE, got:\n%s", body)
	}
}

func TestOpenAIBridge_ReasoningContentStreaming(t *testing.T) {
	mock := &thinkingMockAdapter{
		id:       "mock-reasoning",
		thinking: "Evaluating time complexity O(N)...",
		content:  "The algorithm runs in linear time.",
	}
	pool := router.NewAccountPoolRouter([]types.ProviderAdapter{mock})

	reqBody := []byte(`{
		"model": "gpt-4o",
		"stream": true,
		"messages": [
			{"role": "user", "content": "Analyze algorithm complexity"}
		]
	}`)

	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(reqBody))
	w := httptest.NewRecorder()

	bridge.HandleChatCompletions(w, r, pool)

	body := w.Body.String()
	if !strings.Contains(body, `"reasoning_content":"Evaluating time complexity O(N)..."`) {
		t.Errorf("expected reasoning_content in OpenAI SSE, got:\n%s", body)
	}
	if !strings.Contains(body, `"content":"The algorithm runs in linear time."`) {
		t.Errorf("expected content in OpenAI SSE, got:\n%s", body)
	}
}

func TestGeminiBridge_ThoughtStreaming(t *testing.T) {
	mock := &thinkingMockAdapter{
		id:       "mock-gemini-thought",
		thinking: "Step-by-step mathematical reasoning...",
		content:  "Result is 42.",
	}
	pool := router.NewAccountPoolRouter([]types.ProviderAdapter{mock})

	reqBody := []byte(`{
		"contents": [
			{"role": "user", "parts": [{"text": "What is the answer?"}]}
		]
	}`)

	r := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-flash:streamGenerateContent?alt=sse", bytes.NewReader(reqBody))
	w := httptest.NewRecorder()

	bridge.HandleGeminiGenerateContent(w, r, pool)

	body := w.Body.String()
	if !strings.Contains(body, `"thought":true`) {
		t.Errorf("expected thought: true in Gemini SSE, got:\n%s", body)
	}
	if !strings.Contains(body, "Step-by-step mathematical reasoning...") {
		t.Errorf("expected thought text in Gemini SSE, got:\n%s", body)
	}
	if !strings.Contains(body, "Result is 42.") {
		t.Errorf("expected content in Gemini SSE, got:\n%s", body)
	}
}

func TestClaudeBridge_ThinkingNonStreaming(t *testing.T) {
	mock := &thinkingMockAdapter{
		id:       "mock-thinking-nonstream",
		thinking: "Reasoning in non-stream mode...",
		content:  "Non-stream result.",
	}
	pool := router.NewAccountPoolRouter([]types.ProviderAdapter{mock})

	reqBody := []byte(`{
		"model": "claude-3-7-sonnet-20250219",
		"stream": false,
		"messages": [
			{"role": "user", "content": "Explain quantum computing"}
		]
	}`)

	r := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(reqBody))
	w := httptest.NewRecorder()

	err := bridge.HandleClaudeMessages(w, r, pool, reqBody)
	if err != nil {
		t.Fatalf("HandleClaudeMessages: %v", err)
	}

	var resp struct {
		Content []struct {
			Type     string `json:"type"`
			Thinking string `json:"thinking,omitempty"`
			Text     string `json:"text,omitempty"`
		} `json:"content"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v\nBody: %s", err, w.Body.String())
	}

	if len(resp.Content) < 2 {
		t.Fatalf("expected at least 2 content blocks (thinking + text), got %d: %s", len(resp.Content), w.Body.String())
	}
	if resp.Content[0].Type != "thinking" || resp.Content[0].Thinking != "Reasoning in non-stream mode..." {
		t.Errorf("expected block 0 to be thinking, got %+v", resp.Content[0])
	}
	if resp.Content[1].Type != "text" || resp.Content[1].Text != "Non-stream result." {
		t.Errorf("expected block 1 to be text, got %+v", resp.Content[1])
	}
}
