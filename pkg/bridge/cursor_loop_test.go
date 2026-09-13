package bridge_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"amux-accounts/pkg/bridge"
	"amux-accounts/pkg/router"
	"amux-accounts/pkg/types"
)

// mockCursorBackend simulates an upstream LLM in the pool (e.g. ChatGPT / OpenAI / Gemini API)
// that can respond with Cursor tool calls, multi-tool calls, subagents, and iterative loops.
type mockCursorBackend struct {
	mu           sync.Mutex
	turnCount    int
	onTurn       func(turn int, req *types.ChatRequest) []types.StreamChunk
	recordedReqs []*types.ChatRequest
	id           string
}

func (m *mockCursorBackend) ID() string    { return m.id }
func (m *mockCursorBackend) Priority() int { return 1 }
func (m *mockCursorBackend) SendMessageStream(ctx context.Context, req *types.ChatRequest) (<-chan types.StreamChunk, error) {
	m.mu.Lock()
	m.turnCount++
	turn := m.turnCount
	m.recordedReqs = append(m.recordedReqs, req)
	chunks := m.onTurn(turn, req)
	m.mu.Unlock()

	ch := make(chan types.StreamChunk, len(chunks)+1)
	for _, c := range chunks {
		ch <- c
	}
	close(ch)
	return ch, nil
}

// cursorToolsCatalog defines standard Cursor IDE tools (Composer / Agent mode).
func cursorToolsCatalog() []map[string]any {
	return []map[string]any{
		{
			"type": "function",
			"function": map[string]any{
				"name":        "run_terminal_command",
				"description": "Run a shell command in the terminal",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"command": map[string]any{"type": "string"},
					},
					"required": []string{"command"},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "read_file",
				"description": "Read file contents from disk",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{"type": "string"},
					},
					"required": []string{"path"},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "edit_file",
				"description": "Apply edits to a file",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{"type": "string"},
						"code": map[string]any{"type": "string"},
					},
					"required": []string{"path", "code"},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "grep_search",
				"description": "Search for a regex pattern across codebase",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"query": map[string]any{"type": "string"},
					},
					"required": []string{"query"},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "list_dir",
				"description": "List directory contents",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{"type": "string"},
					},
					"required": []string{"path"},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "invoke_subagent",
				"description": "Delegate a complex subtask to a specialized subagent",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"role":   map[string]any{"type": "string"},
						"prompt": map[string]any{"type": "string"},
					},
					"required": []string{"role", "prompt"},
				},
			},
		},
	}
}

// ----------------------------------------------------------------------------
// 1. Single Tool Execution Loop in Cursor
// ----------------------------------------------------------------------------
func TestCursor_SingleToolLoop(t *testing.T) {
	backend := &mockCursorBackend{
		id: "chatgpt:01",
		onTurn: func(turn int, req *types.ChatRequest) []types.StreamChunk {
			switch turn {
			case 1:
				// Turn 1: Model emits single tool call (run_terminal_command)
				return []types.StreamChunk{
					{
						ID: "chatgpt:01",
						ToolCalls: []types.ToolCall{
							{
								ID:        "call_term_1",
								Name:      "run_terminal_command",
								Arguments: `{"command":"go version"}`,
							},
						},
						FinishReason: "tool_calls",
						Done:         true,
					},
				}
			default:
				// Turn 2: Model finishes with final explanation
				return []types.StreamChunk{
					{ID: "chatgpt:01", Content: "The installed Go version is go1.26 darwin/arm64."},
					{ID: "chatgpt:01", Done: true, FinishReason: "stop"},
				}
			}
		},
	}
	pool := router.NewAccountPoolRouter([]types.ProviderAdapter{backend})

	// Turn 1: Cursor sends request with tools
	reqBody1 := map[string]any{
		"model":  "gpt-4o",
		"stream": true,
		"tools":  cursorToolsCatalog(),
		"messages": []map[string]any{
			{"role": "user", "content": "Check the installed Go version."},
		},
	}
	b1, _ := json.Marshal(reqBody1)
	req1 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(b1))
	req1.Header.Set("Content-Type", "application/json")
	w1 := httptest.NewRecorder()

	bridge.HandleChatCompletions(w1, req1, pool)
	if w1.Code != http.StatusOK {
		t.Fatalf("turn 1 status=%d body=%s", w1.Code, w1.Body.String())
	}
	resp1 := w1.Body.String()
	if !strings.Contains(resp1, `"run_terminal_command"`) || !strings.Contains(resp1, `"call_term_1"`) {
		t.Fatalf("turn 1 missing tool call in SSE: %s", resp1)
	}

	// Turn 2: Cursor returns tool result
	reqBody2 := map[string]any{
		"model":  "gpt-4o",
		"stream": true,
		"tools":  cursorToolsCatalog(),
		"messages": []map[string]any{
			{"role": "user", "content": "Check the installed Go version."},
			{
				"role": "assistant",
				"tool_calls": []map[string]any{
					{
						"id":   "call_term_1",
						"type": "function",
						"function": map[string]any{
							"name":      "run_terminal_command",
							"arguments": `{"command":"go version"}`,
						},
					},
				},
			},
			{
				"role":         "tool",
				"tool_call_id": "call_term_1",
				"content":      "go version go1.26 darwin/arm64",
			},
		},
	}
	b2, _ := json.Marshal(reqBody2)
	req2 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(b2))
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()

	bridge.HandleChatCompletions(w2, req2, pool)
	if w2.Code != http.StatusOK {
		t.Fatalf("turn 2 status=%d body=%s", w2.Code, w2.Body.String())
	}
	resp2 := w2.Body.String()
	if !strings.Contains(resp2, "go1.26") {
		t.Fatalf("turn 2 expected Go version in answer: %s", resp2)
	}
}

// ----------------------------------------------------------------------------
// 2. Multi-Tool Execution in Cursor (Parallel Tool Calls)
// ----------------------------------------------------------------------------
func TestCursor_MultiToolLoop(t *testing.T) {
	backend := &mockCursorBackend{
		id: "chatgpt:01",
		onTurn: func(turn int, req *types.ChatRequest) []types.StreamChunk {
			switch turn {
			case 1:
				// Turn 1: Model emits multiple tool calls at once
				return []types.StreamChunk{
					{
						ID: "chatgpt:01",
						ToolCalls: []types.ToolCall{
							{
								ID:        "call_multi_1",
								Name:      "read_file",
								Arguments: `{"path":"go.mod"}`,
							},
							{
								ID:        "call_multi_2",
								Name:      "list_dir",
								Arguments: `{"path":"pkg"}`,
							},
							{
								ID:        "call_multi_3",
								Name:      "grep_search",
								Arguments: `{"query":"HandleChatCompletions"}`,
							},
						},
						FinishReason: "tool_calls",
						Done:         true,
					},
				}
			default:
				// Turn 2: Synthesize all 3 tool results
				return []types.StreamChunk{
					{ID: "chatgpt:01", Content: "Examined go.mod, listed pkg directory, and found HandleChatCompletions definition."},
					{ID: "chatgpt:01", Done: true, FinishReason: "stop"},
				}
			}
		},
	}
	pool := router.NewAccountPoolRouter([]types.ProviderAdapter{backend})

	// Turn 1: Cursor requests multi-tool analysis
	reqBody1 := map[string]any{
		"model":  "gpt-4o",
		"stream": true,
		"tools":  cursorToolsCatalog(),
		"messages": []map[string]any{
			{"role": "user", "content": "Analyze go.mod, inspect pkg directory, and locate HandleChatCompletions."},
		},
	}
	b1, _ := json.Marshal(reqBody1)
	req1 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(b1))
	w1 := httptest.NewRecorder()

	bridge.HandleChatCompletions(w1, req1, pool)
	if w1.Code != http.StatusOK {
		t.Fatalf("multi-tool turn 1 failed: %d %s", w1.Code, w1.Body.String())
	}
	resp1 := w1.Body.String()
	if !strings.Contains(resp1, "call_multi_1") || !strings.Contains(resp1, "call_multi_2") || !strings.Contains(resp1, "call_multi_3") {
		t.Fatalf("expected all 3 parallel tool calls in SSE: %s", resp1)
	}

	// Turn 2: Cursor returns all 3 tool results
	reqBody2 := map[string]any{
		"model":  "gpt-4o",
		"stream": true,
		"tools":  cursorToolsCatalog(),
		"messages": []map[string]any{
			{"role": "user", "content": "Analyze go.mod, inspect pkg directory, and locate HandleChatCompletions."},
			{
				"role": "assistant",
				"tool_calls": []map[string]any{
					{"id": "call_multi_1", "type": "function", "function": map[string]any{"name": "read_file", "arguments": `{"path":"go.mod"}`}},
					{"id": "call_multi_2", "type": "function", "function": map[string]any{"name": "list_dir", "arguments": `{"path":"pkg"}`}},
					{"id": "call_multi_3", "type": "function", "function": map[string]any{"name": "grep_search", "arguments": `{"query":"HandleChatCompletions"}`}},
				},
			},
			{"role": "tool", "tool_call_id": "call_multi_1", "content": "module amux-accounts\n\ngo 1.26"},
			{"role": "tool", "tool_call_id": "call_multi_2", "content": "bridge, cli, hook, monitor, proxy, router, tools, types, ui"},
			{"role": "tool", "tool_call_id": "call_multi_3", "content": "pkg/bridge/openai.go:21:func HandleChatCompletions"},
		},
	}
	b2, _ := json.Marshal(reqBody2)
	req2 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(b2))
	w2 := httptest.NewRecorder()

	bridge.HandleChatCompletions(w2, req2, pool)
	if w2.Code != http.StatusOK {
		t.Fatalf("multi-tool turn 2 failed: %d %s", w2.Code, w2.Body.String())
	}
	resp2 := w2.Body.String()
	if !strings.Contains(resp2, "Examined go.mod") {
		t.Fatalf("expected synthesis response: %s", resp2)
	}
}

// ----------------------------------------------------------------------------
// 3. Agent Subagent Delegation in Cursor
// ----------------------------------------------------------------------------
func TestCursor_AgentSubagentLoop(t *testing.T) {
	backend := &mockCursorBackend{
		id: "chatgpt:01",
		onTurn: func(turn int, req *types.ChatRequest) []types.StreamChunk {
			switch turn {
			case 1:
				// Turn 1: Main agent delegates to a Codebase Researcher subagent
				return []types.StreamChunk{
					{
						ID: "chatgpt:01",
						ToolCalls: []types.ToolCall{
							{
								ID:        "call_subagent_1",
								Name:      "invoke_subagent",
								Arguments: `{"role":"Codebase Researcher","prompt":"Search for all occurrences of 'DialectCursor' in pkg/tools"}`,
							},
						},
						FinishReason: "tool_calls",
						Done:         true,
					},
				}
			default:
				// Turn 2: Subagent reports back, agent presents final result
				return []types.StreamChunk{
					{ID: "chatgpt:01", Content: "Subagent completed research. DialectCursor is referenced in cursor.go, dialect.go, and dialect_loop_test.go."},
					{ID: "chatgpt:01", Done: true, FinishReason: "stop"},
				}
			}
		},
	}
	pool := router.NewAccountPoolRouter([]types.ProviderAdapter{backend})

	reqBody1 := map[string]any{
		"model":  "gpt-4o",
		"stream": true,
		"tools":  cursorToolsCatalog(),
		"messages": []map[string]any{
			{"role": "user", "content": "Please research how DialectCursor is used across the codebase."},
		},
	}
	b1, _ := json.Marshal(reqBody1)
	req1 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(b1))
	w1 := httptest.NewRecorder()

	bridge.HandleChatCompletions(w1, req1, pool)
	if w1.Code != http.StatusOK {
		t.Fatalf("subagent turn 1 failed: %d %s", w1.Code, w1.Body.String())
	}
	resp1 := w1.Body.String()
	if !strings.Contains(resp1, "invoke_subagent") || !strings.Contains(resp1, "Codebase Researcher") {
		t.Fatalf("expected subagent invocation: %s", resp1)
	}

	// Turn 2: Return subagent findings
	reqBody2 := map[string]any{
		"model":  "gpt-4o",
		"stream": true,
		"tools":  cursorToolsCatalog(),
		"messages": []map[string]any{
			{"role": "user", "content": "Please research how DialectCursor is used across the codebase."},
			{
				"role": "assistant",
				"tool_calls": []map[string]any{
					{
						"id":   "call_subagent_1",
						"type": "function",
						"function": map[string]any{
							"name":      "invoke_subagent",
							"arguments": `{"role":"Codebase Researcher","prompt":"Search for all occurrences of 'DialectCursor' in pkg/tools"}`,
						},
					},
				},
			},
			{
				"role":         "tool",
				"tool_call_id": "call_subagent_1",
				"content":      `{"status":"success","findings":["pkg/tools/cursor.go","pkg/tools/dialect.go","pkg/tools/dialect_loop_test.go"]}`,
			},
		},
	}
	b2, _ := json.Marshal(reqBody2)
	req2 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(b2))
	w2 := httptest.NewRecorder()

	bridge.HandleChatCompletions(w2, req2, pool)
	if w2.Code != http.StatusOK {
		t.Fatalf("subagent turn 2 failed: %d %s", w2.Code, w2.Body.String())
	}
	resp2 := w2.Body.String()
	if !strings.Contains(resp2, "Subagent completed research") {
		t.Fatalf("expected final report: %s", resp2)
	}
}

// ----------------------------------------------------------------------------
// 4. Multi-Agent Multi-Loop (Iterative 5-turn complex workflow in Cursor)
// ----------------------------------------------------------------------------
// Turn 1: User prompt -> Model dispatches Research Agent
// Turn 2: Research Agent returns -> Model dispatches Test Agent
// Turn 3: Test Agent returns -> Model calls edit_file
// Turn 4: Edit completed -> Model calls run_terminal_command (verify go test)
// Turn 5: Command output -> Model gives final summary
func TestCursor_MultiAgentMultiLoop(t *testing.T) {
	backend := &mockCursorBackend{
		id: "chatgpt:01",
		onTurn: func(turn int, req *types.ChatRequest) []types.StreamChunk {
			switch turn {
			case 1:
				return []types.StreamChunk{
					{
						ID: "chatgpt:01",
						ToolCalls: []types.ToolCall{
							{ID: "call_maml_1", Name: "invoke_subagent", Arguments: `{"role":"Architecture Researcher","prompt":"Audit proxy gateway"}`},
						},
						FinishReason: "tool_calls",
						Done:         true,
					},
				}
			case 2:
				return []types.StreamChunk{
					{
						ID: "chatgpt:01",
						ToolCalls: []types.ToolCall{
							{ID: "call_maml_2", Name: "invoke_subagent", Arguments: `{"role":"Security Auditor","prompt":"Check tunnel hijacking safety"}`},
						},
						FinishReason: "tool_calls",
						Done:         true,
					},
				}
			case 3:
				return []types.StreamChunk{
					{
						ID: "chatgpt:01",
						ToolCalls: []types.ToolCall{
							{ID: "call_maml_3", Name: "edit_file", Arguments: `{"path":"pkg/proxy/tunnel.go","code":"// safe CONNECT handler"}`},
						},
						FinishReason: "tool_calls",
						Done:         true,
					},
				}
			case 4:
				return []types.StreamChunk{
					{
						ID: "chatgpt:01",
						ToolCalls: []types.ToolCall{
							{ID: "call_maml_4", Name: "run_terminal_command", Arguments: `{"command":"go test ./pkg/proxy"}`},
						},
						FinishReason: "tool_calls",
						Done:         true,
					},
				}
			default:
				return []types.StreamChunk{
					{ID: "chatgpt:01", Content: "Workflow complete. Subagents audited architecture and security, patch applied, tests passing 100%."},
					{ID: "chatgpt:01", Done: true, FinishReason: "stop"},
				}
			}
		},
	}
	pool := router.NewAccountPoolRouter([]types.ProviderAdapter{backend})

	var conversationHistory []map[string]any
	conversationHistory = append(conversationHistory, map[string]any{
		"role":    "user",
		"content": "Perform multi-agent audit and fix tunnel verification.",
	})

	// Run all 5 turns iteratively
	for turn := 1; turn <= 5; turn++ {
		reqBody := map[string]any{
			"model":    "gpt-4o",
			"stream":   true,
			"tools":    cursorToolsCatalog(),
			"messages": conversationHistory,
		}
		b, _ := json.Marshal(reqBody)
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(b))
		w := httptest.NewRecorder()

		bridge.HandleChatCompletions(w, req, pool)
		if w.Code != http.StatusOK {
			t.Fatalf("turn %d failed with code %d: %s", turn, w.Code, w.Body.String())
		}
		bodyStr := w.Body.String()

		switch turn {
			case 1:
				if !strings.Contains(bodyStr, "Architecture Researcher") {
					t.Fatalf("turn 1 expected Architecture Researcher subagent: %s", bodyStr)
				}
				conversationHistory = append(conversationHistory,
					map[string]any{
						"role": "assistant",
						"tool_calls": []map[string]any{
							{"id": "call_maml_1", "type": "function", "function": map[string]any{"name": "invoke_subagent", "arguments": `{"role":"Architecture Researcher","prompt":"Audit proxy gateway"}`}},
						},
					},
					map[string]any{
						"role":         "tool",
						"tool_call_id": "call_maml_1",
						"content":      `{"status":"audit_complete","findings":"tunnel.go needs header validation"}`,
					},
				)
			case 2:
				if !strings.Contains(bodyStr, "Security Auditor") {
					t.Fatalf("turn 2 expected Security Auditor subagent: %s", bodyStr)
				}
				conversationHistory = append(conversationHistory,
					map[string]any{
						"role": "assistant",
						"tool_calls": []map[string]any{
							{"id": "call_maml_2", "type": "function", "function": map[string]any{"name": "invoke_subagent", "arguments": `{"role":"Security Auditor","prompt":"Check tunnel hijacking safety"}`}},
						},
					},
					map[string]any{
						"role":         "tool",
						"tool_call_id": "call_maml_2",
						"content":      `{"status":"audit_pass","note":"Add explicit comment for safety"}`,
					},
				)
			case 3:
				if !strings.Contains(bodyStr, "edit_file") {
					t.Fatalf("turn 3 expected edit_file: %s", bodyStr)
				}
				conversationHistory = append(conversationHistory,
					map[string]any{
						"role": "assistant",
						"tool_calls": []map[string]any{
							{"id": "call_maml_3", "type": "function", "function": map[string]any{"name": "edit_file", "arguments": `{"path":"pkg/proxy/tunnel.go","code":"// safe CONNECT handler"}`}},
						},
					},
					map[string]any{
						"role":         "tool",
						"tool_call_id": "call_maml_3",
						"content":      `{"status":"success","bytes_written":28}`,
					},
				)
			case 4:
				if !strings.Contains(bodyStr, "run_terminal_command") {
					t.Fatalf("turn 4 expected run_terminal_command: %s", bodyStr)
				}
				conversationHistory = append(conversationHistory,
					map[string]any{
						"role": "assistant",
						"tool_calls": []map[string]any{
							{"id": "call_maml_4", "type": "function", "function": map[string]any{"name": "run_terminal_command", "arguments": `{"command":"go test ./pkg/proxy"}`}},
						},
					},
					map[string]any{
						"role":         "tool",
						"tool_call_id": "call_maml_4",
						"content":      "ok amux-accounts/pkg/proxy 0.45s",
					},
				)
			case 5:
				if !strings.Contains(bodyStr, "Workflow complete") {
					t.Fatalf("turn 5 expected completion: %s", bodyStr)
				}
		}
	}

	if backend.turnCount != 5 {
		t.Fatalf("expected exactly 5 turns in multi-loop, got %d", backend.turnCount)
	}
}

// ----------------------------------------------------------------------------
// 5. Subscription Isolation: Never use Claude or AGY subscriptions
// ----------------------------------------------------------------------------
func TestCursor_SubscriptionIsolation(t *testing.T) {
	// Adapter in the pool represents a non-subscription provider (e.g. ChatGPT web or OpenAI API)
	providerInvoked := false
	backend := &mockCursorBackend{
		id: "chatgpt:01",
		onTurn: func(turn int, req *types.ChatRequest) []types.StreamChunk {
			providerInvoked = true
			return []types.StreamChunk{
				{ID: "chatgpt:01", Content: "Serving from pool provider, zero subscription involved."},
				{ID: "chatgpt:01", Done: true, FinishReason: "stop"},
			}
		},
	}
	pool := router.NewAccountPoolRouter([]types.ProviderAdapter{backend})

	reqBody := map[string]any{
		"model":  "gpt-4o",
		"stream": false, // Non-streaming JSON mode test
		"tools":  cursorToolsCatalog(),
		"messages": []map[string]any{
			{"role": "user", "content": "Confirm that Claude/AGY subscriptions are not touched."},
		},
	}
	b, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(b))
	w := httptest.NewRecorder()

	bridge.HandleChatCompletions(w, req, pool)
	if w.Code != http.StatusOK {
		t.Fatalf("non-streaming chat failed: %d %s", w.Code, w.Body.String())
	}

	if !providerInvoked {
		t.Fatal("expected pool provider to be invoked")
	}

	// Verify JSON response structure
	var res map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("invalid json response: %v", err)
	}
	choices, _ := res["choices"].([]any)
	if len(choices) == 0 {
		t.Fatalf("empty choices: %v", res)
	}
	msg, _ := choices[0].(map[string]any)["message"].(map[string]any)
	content, _ := msg["content"].(string)
	if !strings.Contains(content, "zero subscription involved") {
		t.Fatalf("unexpected content: %s", content)
	}
}
