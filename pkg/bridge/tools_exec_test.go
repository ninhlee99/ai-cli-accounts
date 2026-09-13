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

type toolCallAdapter struct {
	id    string
	calls []types.ToolCall
	text  string
}

func (m *toolCallAdapter) ID() string    { return m.id }
func (m *toolCallAdapter) Priority() int { return 1 }
func (m *toolCallAdapter) SendMessageStream(ctx context.Context, req *types.ChatRequest) (<-chan types.StreamChunk, error) {
	// Assert tools arrived from client (Claude / Cursor / Codex path).
	ch := make(chan types.StreamChunk, 4)
	go func() {
		defer close(ch)
		if m.text != "" {
			ch <- types.StreamChunk{ID: m.id, Content: m.text}
		}
		if len(m.calls) > 0 {
			ch <- types.StreamChunk{ID: m.id, ToolCalls: m.calls, FinishReason: "tool_calls", Done: true}
			return
		}
		ch <- types.StreamChunk{ID: m.id, Done: true, FinishReason: "stop"}
	}()
	return ch, nil
}

func TestClaudeBridge_EmitsToolUseForClientExecution(t *testing.T) {
	adapter := &toolCallAdapter{
		id: "pool:01",
		calls: []types.ToolCall{
			{ID: "toolu_bash", Name: "Bash", Arguments: `{"command":"ls"}`},
		},
	}
	pool := router.NewAccountPoolRouter([]types.ProviderAdapter{adapter})

	body := []byte(`{
		"model":"claude-sonnet-4-20250514",
		"stream":false,
		"tools":[{"name":"Bash","description":"shell","input_schema":{"type":"object","properties":{"command":{"type":"string"}}}}],
		"messages":[{"role":"user","content":"list files"}]
	}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	if err := bridge.HandleClaudeMessages(rec, req, pool, body); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	var resp struct {
		StopReason string `json:"stop_reason"`
		Content    []struct {
			Type  string          `json:"type"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
			Text  string          `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.StopReason != "tool_use" {
		t.Fatalf("stop_reason=%q want tool_use", resp.StopReason)
	}
	found := false
	for _, c := range resp.Content {
		if c.Type == "tool_use" && c.Name == "Bash" && c.ID == "toolu_bash" {
			found = true
			if string(c.Input) != `{"command":"ls"}` {
				t.Fatalf("input=%s", c.Input)
			}
		}
	}
	if !found {
		t.Fatalf("missing tool_use in content=%+v", resp.Content)
	}
}

func TestClaudeBridge_AcceptsToolResultsFromClient(t *testing.T) {
	// After Claude Code executes Bash locally, it posts tool_result blocks.
	seenTools := 0
	adapter := &toolCallAdapter{id: "pool:01", text: "done"}
	// wrap to inspect request
	pool := router.NewAccountPoolRouter([]types.ProviderAdapter{
		&inspectAdapter{inner: adapter, onReq: func(req *types.ChatRequest) {
			seenTools = len(req.Tools)
			hasToolRole := false
			for _, m := range req.Messages {
				if m.Role == "tool" && m.ToolCallID == "toolu_bash" && m.Content == "README.md\n" {
					hasToolRole = true
				}
			}
			if !hasToolRole {
				t.Errorf("expected tool result message in canonical req: %+v", req.Messages)
			}
		}},
	})

	body := []byte(`{
		"model":"claude-sonnet-4-20250514",
		"stream":false,
		"tools":[{"name":"Bash","input_schema":{"type":"object","properties":{}}}],
		"messages":[{
			"role":"user",
			"content":[
				{"type":"tool_result","tool_use_id":"toolu_bash","content":"README.md\n"},
				{"type":"text","text":"summarize"}
			]
		}]
	}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	if err := bridge.HandleClaudeMessages(rec, req, pool, body); err != nil {
		t.Fatal(err)
	}
	if seenTools != 1 {
		t.Fatalf("tools not passed to pool, seen=%d", seenTools)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d %s", rec.Code, rec.Body.String())
	}
}

type inspectAdapter struct {
	inner *toolCallAdapter
	onReq func(*types.ChatRequest)
}

func (a *inspectAdapter) ID() string    { return a.inner.ID() }
func (a *inspectAdapter) Priority() int { return a.inner.Priority() }
func (a *inspectAdapter) SendMessageStream(ctx context.Context, req *types.ChatRequest) (<-chan types.StreamChunk, error) {
	if a.onReq != nil {
		a.onReq(req)
	}
	return a.inner.SendMessageStream(ctx, req)
}

func TestCursorCodexBridge_EmitsToolCalls(t *testing.T) {
	adapter := &toolCallAdapter{
		id: "cursor-pool",
		calls: []types.ToolCall{
			{ID: "call_1", Name: "Read", Arguments: `{"path":"a.go"}`},
		},
	}
	pool := router.NewAccountPoolRouter([]types.ProviderAdapter{adapter})

	body := `{
		"model":"gpt-4o",
		"stream":false,
		"tools":[{"type":"function","function":{"name":"Read","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}}],
		"messages":[{"role":"user","content":"read a.go"}]
	}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	bridge.HandleChatCompletions(rec, req, pool)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				ToolCalls []struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Choices) != 1 || resp.Choices[0].FinishReason != "tool_calls" {
		t.Fatalf("resp=%s", rec.Body.String())
	}
	tc := resp.Choices[0].Message.ToolCalls
	if len(tc) != 1 || tc[0].Function.Name != "Read" || tc[0].Function.Arguments != `{"path":"a.go"}` {
		t.Fatalf("tool_calls=%+v", tc)
	}
}

func TestCursorBridge_StreamingToolCalls(t *testing.T) {
	adapter := &toolCallAdapter{
		id: "cursor-stream",
		calls: []types.ToolCall{
			{ID: "call_stream", Name: "Bash", Arguments: `{"command":"pwd"}`},
		},
	}
	pool := router.NewAccountPoolRouter([]types.ProviderAdapter{adapter})
	body := `{"model":"gpt-4o","stream":true,"tools":[{"type":"function","function":{"name":"Bash","parameters":{"type":"object"}}}],"messages":[{"role":"user","content":"pwd"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	bridge.HandleChatCompletions(rec, req, pool)
	out := rec.Body.String()
	if !strings.Contains(out, `"finish_reason":"tool_calls"`) {
		t.Fatalf("missing tool_calls finish: %s", out)
	}
	if !strings.Contains(out, `"name":"Bash"`) || !strings.Contains(out, "pwd") {
		t.Fatalf("missing tool delta: %s", out)
	}
	if !strings.HasSuffix(strings.TrimSpace(out), "data: [DONE]") {
		t.Fatalf("missing DONE: %s", out)
	}
}

func TestClaudeBridge_StreamEmitsStartAndPing(t *testing.T) {
	adapter := &toolCallAdapter{id: "chatgpt:01", text: "ok"}
	pool := router.NewAccountPoolRouter([]types.ProviderAdapter{adapter})
	body := []byte(`{"model":"claude-opus-5","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	if err := bridge.HandleClaudeMessages(rec, req, pool, body); err != nil {
		t.Fatal(err)
	}
	out := rec.Body.String()
	if !strings.Contains(out, "event: message_start") {
		t.Fatalf("missing message_start: %s", out)
	}
	if !strings.Contains(out, "event: ping") {
		t.Fatalf("missing ping: %s", out)
	}
	if !strings.Contains(out, "event: message_stop") {
		t.Fatalf("missing message_stop: %s", out)
	}
}
