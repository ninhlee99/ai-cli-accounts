package bridge_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-cli-accounts/pkg/bridge"
	"ai-cli-accounts/pkg/router"
	"ai-cli-accounts/pkg/types"
)

type mockStreamAdapter struct {
	id     string
	chunks []types.StreamChunk
}

func (m *mockStreamAdapter) ID() string    { return m.id }
func (m *mockStreamAdapter) Priority() int { return 1 }
func (m *mockStreamAdapter) SendMessageStream(ctx context.Context, req *types.ChatRequest) (<-chan types.StreamChunk, error) {
	ch := make(chan types.StreamChunk, len(m.chunks))
	for _, c := range m.chunks {
		ch <- c
	}
	close(ch)
	return ch, nil
}

func TestHandleChatCompletions_Streaming(t *testing.T) {
	adapter := &mockStreamAdapter{
		id: "test-adapter",
		chunks: []types.StreamChunk{
			{ID: "test-adapter", Content: "Hello "},
			{ID: "test-adapter", Content: "world!"},
			{ID: "test-adapter", Done: true},
		},
	}
	pool := router.NewAccountPoolRouter([]types.ProviderAdapter{adapter})

	reqBody := `{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(reqBody))
	rec := httptest.NewRecorder()

	bridge.HandleChatCompletions(rec, req, pool)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	body := rec.Body.String()
	if !strings.Contains(body, "Hello ") || !strings.Contains(body, "world!") {
		t.Errorf("missing content in stream body: %s", body)
	}
	if !strings.Contains(body, `"finish_reason":"stop"`) {
		t.Errorf("expected finish_reason stop chunk, got: %s", body)
	}
	if !strings.HasSuffix(strings.TrimSpace(body), "data: [DONE]") {
		t.Errorf("expected stream to end with data: [DONE], got: %s", body)
	}
}

func TestHandleChatCompletions_NonStreaming(t *testing.T) {
	adapter := &mockStreamAdapter{
		id: "test-adapter",
		chunks: []types.StreamChunk{
			{ID: "test-adapter", Content: "Non-streaming "},
			{ID: "test-adapter", Content: "response"},
			{ID: "test-adapter", Done: true},
		},
	}
	pool := router.NewAccountPoolRouter([]types.ProviderAdapter{adapter})

	reqBody := `{"model":"gpt-4o","stream":false,"messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(reqBody))
	rec := httptest.NewRecorder()

	bridge.HandleChatCompletions(rec, req, pool)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Choices []struct {
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(resp.Choices) == 0 {
		t.Fatalf("no choices returned")
	}
	if resp.Choices[0].Message.Content != "Non-streaming response" {
		t.Errorf("unexpected content: %s", resp.Choices[0].Message.Content)
	}
	if resp.Choices[0].FinishReason != "stop" {
		t.Errorf("unexpected finish_reason: %s", resp.Choices[0].FinishReason)
	}
}
