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

// mockCodexBackend simulates an upstream LLM in the pool (e.g. ChatGPT / OpenAI / Gemini API)
// that can respond with Codex tool calls, multi-tool calls, subagents, and iterative loops.
type mockCodexBackend struct {
	mu           sync.Mutex
	turnCount    int
	onTurn       func(turn int, req *types.ChatRequest) []types.StreamChunk
	recordedReqs []*types.ChatRequest
	id           string
}

func (m *mockCodexBackend) ID() string    { return m.id }
func (m *mockCodexBackend) Priority() int { return 1 }
func (m *mockCodexBackend) SendMessageStream(ctx context.Context, req *types.ChatRequest) (<-chan types.StreamChunk, error) {
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

// codexToolsCatalog defines standard Codex CLI tools (Agent & Automation mode).
func codexToolsCatalog() []map[string]any {
	return []map[string]any{
		{
			"type": "function",
			"function": map[string]any{
				"name":        "exec_command",
				"description": "Execute a shell command locally in the workspace",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"cmd": map[string]any{"type": "string"},
					},
					"required": []string{"cmd"},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "read_file",
				"description": "Read file contents from local filesystem",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"file_path": map[string]any{"type": "string"},
					},
					"required": []string{"file_path"},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "apply_diff",
				"description": "Apply a unified diff patch to a target file",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"file_path": map[string]any{"type": "string"},
						"diff":      map[string]any{"type": "string"},
					},
					"required": []string{"file_path", "diff"},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "list_dir",
				"description": "List files and directories within a directory",
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
				"name":        "grep_search",
				"description": "Search directory recursively using regular expression pattern",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"pattern": map[string]any{"type": "string"},
					},
					"required": []string{"pattern"},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "invoke_subagent",
				"description": "Delegate a subtask to an autonomous subagent",
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
// 1. Single Tool Execution Loop in Codex
// ----------------------------------------------------------------------------
func TestCodex_SingleToolLoop(t *testing.T) {
	backend := &mockCodexBackend{
		id: "chatgpt:01",
		onTurn: func(turn int, req *types.ChatRequest) []types.StreamChunk {
			switch turn {
			case 1:
				return []types.StreamChunk{
					{
						ID: "chatgpt:01",
						ToolCalls: []types.ToolCall{
							{
								ID:        "call_codex_term_1",
								Name:      "exec_command",
								Arguments: `{"cmd":"git rev-parse --short HEAD"}`,
							},
						},
						FinishReason: "tool_calls",
						Done:         true,
					},
				}
			default:
				return []types.StreamChunk{
					{ID: "chatgpt:01", Content: "Current commit hash is f1b800e."},
					{ID: "chatgpt:01", Done: true, FinishReason: "stop"},
				}
			}
		},
	}
	pool := router.NewAccountPoolRouter([]types.ProviderAdapter{backend})

	// Turn 1: Codex sends prompt with tool definitions
	reqBody1 := map[string]any{
		"model":  "gpt-4o",
		"stream": true,
		"tools":  codexToolsCatalog(),
		"messages": []map[string]any{
			{"role": "user", "content": "Get the current git commit hash."},
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
	if !strings.Contains(resp1, `"exec_command"`) || !strings.Contains(resp1, `"call_codex_term_1"`) {
		t.Fatalf("turn 1 missing tool call in SSE: %s", resp1)
	}

	// Turn 2: Codex client returns tool execution result
	reqBody2 := map[string]any{
		"model":  "gpt-4o",
		"stream": true,
		"tools":  codexToolsCatalog(),
		"messages": []map[string]any{
			{"role": "user", "content": "Get the current git commit hash."},
			{
				"role": "assistant",
				"tool_calls": []map[string]any{
					{
						"id":   "call_codex_term_1",
						"type": "function",
						"function": map[string]any{
							"name":      "exec_command",
							"arguments": `{"cmd":"git rev-parse --short HEAD"}`,
						},
					},
				},
			},
			{
				"role":         "tool",
				"tool_call_id": "call_codex_term_1",
				"content":      "f1b800e\n",
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
	if !strings.Contains(resp2, "f1b800e") {
		t.Fatalf("turn 2 expected commit hash in answer: %s", resp2)
	}
}

// ----------------------------------------------------------------------------
// 2. Multi-Tool Execution in Codex (Parallel Tool Calls)
// ----------------------------------------------------------------------------
func TestCodex_MultiToolLoop(t *testing.T) {
	backend := &mockCodexBackend{
		id: "chatgpt:01",
		onTurn: func(turn int, req *types.ChatRequest) []types.StreamChunk {
			switch turn {
			case 1:
				return []types.StreamChunk{
					{
						ID: "chatgpt:01",
						ToolCalls: []types.ToolCall{
							{
								ID:        "call_cdx_multi_1",
								Name:      "read_file",
								Arguments: `{"file_path":"go.mod"}`,
							},
							{
								ID:        "call_cdx_multi_2",
								Name:      "list_dir",
								Arguments: `{"path":"pkg/tools"}`,
							},
							{
								ID:        "call_cdx_multi_3",
								Name:      "grep_search",
								Arguments: `{"pattern":"ToCodexTools"}`,
							},
						},
						FinishReason: "tool_calls",
						Done:         true,
					},
				}
			default:
				return []types.StreamChunk{
					{ID: "chatgpt:01", Content: "Codex inspected go.mod, listed pkg/tools, and located ToCodexTools in codex.go."},
					{ID: "chatgpt:01", Done: true, FinishReason: "stop"},
				}
			}
		},
	}
	pool := router.NewAccountPoolRouter([]types.ProviderAdapter{backend})

	// Turn 1: Codex requests parallel tool executions
	reqBody1 := map[string]any{
		"model":  "gpt-4o",
		"stream": true,
		"tools":  codexToolsCatalog(),
		"messages": []map[string]any{
			{"role": "user", "content": "Analyze go.mod, check pkg/tools files, and search for ToCodexTools."},
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
	if !strings.Contains(resp1, "call_cdx_multi_1") || !strings.Contains(resp1, "call_cdx_multi_2") || !strings.Contains(resp1, "call_cdx_multi_3") {
		t.Fatalf("expected all 3 parallel tool calls in SSE: %s", resp1)
	}

	// Turn 2: Codex returns all 3 tool results
	reqBody2 := map[string]any{
		"model":  "gpt-4o",
		"stream": true,
		"tools":  codexToolsCatalog(),
		"messages": []map[string]any{
			{"role": "user", "content": "Analyze go.mod, check pkg/tools files, and search for ToCodexTools."},
			{
				"role": "assistant",
				"tool_calls": []map[string]any{
					{"id": "call_cdx_multi_1", "type": "function", "function": map[string]any{"name": "read_file", "arguments": `{"file_path":"go.mod"}`}},
					{"id": "call_cdx_multi_2", "type": "function", "function": map[string]any{"name": "list_dir", "arguments": `{"path":"pkg/tools"}`}},
					{"id": "call_cdx_multi_3", "type": "function", "function": map[string]any{"name": "grep_search", "arguments": `{"pattern":"ToCodexTools"}`}},
				},
			},
			{"role": "tool", "tool_call_id": "call_cdx_multi_1", "content": "module amux-accounts\n\ngo 1.26"},
			{"role": "tool", "tool_call_id": "call_cdx_multi_2", "content": "codex.go, cursor.go, claude.go, gemini.go, openai.go, webloop.go"},
			{"role": "tool", "tool_call_id": "call_cdx_multi_3", "content": "pkg/tools/codex.go:11:func ToCodexTools"},
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
	if !strings.Contains(resp2, "Codex inspected go.mod") {
		t.Fatalf("expected synthesis response: %s", resp2)
	}
}

// ----------------------------------------------------------------------------
// 3. Agent Subagent Delegation in Codex
// ----------------------------------------------------------------------------
func TestCodex_AgentSubagentLoop(t *testing.T) {
	backend := &mockCodexBackend{
		id: "chatgpt:01",
		onTurn: func(turn int, req *types.ChatRequest) []types.StreamChunk {
			switch turn {
			case 1:
				return []types.StreamChunk{
					{
						ID: "chatgpt:01",
						ToolCalls: []types.ToolCall{
							{
								ID:        "call_cdx_subagent_1",
								Name:      "invoke_subagent",
								Arguments: `{"role":"Codex Refactor Specialist","prompt":"Analyze pkg/tools/codex.go and verify JSON schema mapping"}`,
							},
						},
						FinishReason: "tool_calls",
						Done:         true,
					},
				}
			default:
				return []types.StreamChunk{
					{ID: "chatgpt:01", Content: "Subagent completed verification. Schema mapping in codex.go conforms to OpenAI specification."},
					{ID: "chatgpt:01", Done: true, FinishReason: "stop"},
				}
			}
		},
	}
	pool := router.NewAccountPoolRouter([]types.ProviderAdapter{backend})

	reqBody1 := map[string]any{
		"model":  "gpt-4o",
		"stream": true,
		"tools":  codexToolsCatalog(),
		"messages": []map[string]any{
			{"role": "user", "content": "Run schema analysis via subagent."},
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
	if !strings.Contains(resp1, "invoke_subagent") || !strings.Contains(resp1, "Codex Refactor Specialist") {
		t.Fatalf("expected subagent call in SSE: %s", resp1)
	}

	// Turn 2: Feed subagent output back into Codex agent
	reqBody2 := map[string]any{
		"model":  "gpt-4o",
		"stream": true,
		"tools":  codexToolsCatalog(),
		"messages": []map[string]any{
			{"role": "user", "content": "Run schema analysis via subagent."},
			{
				"role": "assistant",
				"tool_calls": []map[string]any{
					{
						"id":   "call_cdx_subagent_1",
						"type": "function",
						"function": map[string]any{
							"name":      "invoke_subagent",
							"arguments": `{"role":"Codex Refactor Specialist","prompt":"Analyze pkg/tools/codex.go and verify JSON schema mapping"}`,
						},
					},
				},
			},
			{
				"role":         "tool",
				"tool_call_id": "call_cdx_subagent_1",
				"content":      `{"status":"verified","details":"All types match standard openAITool"}`,
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
	if !strings.Contains(resp2, "Subagent completed verification") {
		t.Fatalf("expected final synthesis response: %s", resp2)
	}
}

// ----------------------------------------------------------------------------
// 4. Multi-Agent Multi-Loop (5-turn iterative complex workflow in Codex)
// ----------------------------------------------------------------------------
// Turn 1: User prompt -> Model dispatches Codebase Auditor subagent
// Turn 2: Auditor returns -> Model dispatches Performance Optimizer subagent
// Turn 3: Optimizer returns -> Model calls apply_diff
// Turn 4: Diff applied -> Model calls exec_command to run test suite
// Turn 5: Test output -> Model concludes with summary report
func TestCodex_MultiAgentMultiLoop(t *testing.T) {
	backend := &mockCodexBackend{
		id: "chatgpt:01",
		onTurn: func(turn int, req *types.ChatRequest) []types.StreamChunk {
			switch turn {
			case 1:
				return []types.StreamChunk{
					{
						ID: "chatgpt:01",
						ToolCalls: []types.ToolCall{
							{ID: "call_cdx_maml_1", Name: "invoke_subagent", Arguments: `{"role":"Architecture Auditor","prompt":"Inspect codex gateway architecture"}`},
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
							{ID: "call_cdx_maml_2", Name: "invoke_subagent", Arguments: `{"role":"Performance Engineer","prompt":"Benchmark token capture latency"}`},
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
							{ID: "call_cdx_maml_3", Name: "apply_diff", Arguments: `{"file_path":"pkg/tools/codex.go","diff":"@@ -1,3 +1,4 @@\n+// optimized codex dialect"}`},
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
							{ID: "call_cdx_maml_4", Name: "exec_command", Arguments: `{"cmd":"go test ./pkg/tools"}`},
						},
						FinishReason: "tool_calls",
						Done:         true,
					},
				}
			default:
				return []types.StreamChunk{
					{ID: "chatgpt:01", Content: "Codex multi-agent workflow succeeded: architecture audited, performance verified, diff applied, and test suite green."},
					{ID: "chatgpt:01", Done: true, FinishReason: "stop"},
				}
			}
		},
	}
	pool := router.NewAccountPoolRouter([]types.ProviderAdapter{backend})

	var conversationHistory []map[string]any
	conversationHistory = append(conversationHistory, map[string]any{
		"role":    "user",
		"content": "Perform multi-agent audit and performance optimization on codex tools.",
	})

	for turn := 1; turn <= 5; turn++ {
		reqBody := map[string]any{
			"model":    "gpt-4o",
			"stream":   true,
			"tools":    codexToolsCatalog(),
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
			if !strings.Contains(bodyStr, "Architecture Auditor") {
				t.Fatalf("turn 1 expected Architecture Auditor: %s", bodyStr)
			}
			conversationHistory = append(conversationHistory,
				map[string]any{
					"role": "assistant",
					"tool_calls": []map[string]any{
						{"id": "call_cdx_maml_1", "type": "function", "function": map[string]any{"name": "invoke_subagent", "arguments": `{"role":"Architecture Auditor","prompt":"Inspect codex gateway architecture"}`}},
					},
				},
				map[string]any{
					"role":         "tool",
					"tool_call_id": "call_cdx_maml_1",
					"content":      `{"status":"audit_complete","findings":"Codex dialect maps directly to OpenAI tools wire format"}`,
				},
			)
		case 2:
			if !strings.Contains(bodyStr, "Performance Engineer") {
				t.Fatalf("turn 2 expected Performance Engineer: %s", bodyStr)
			}
			conversationHistory = append(conversationHistory,
				map[string]any{
					"role": "assistant",
					"tool_calls": []map[string]any{
						{"id": "call_cdx_maml_2", "type": "function", "function": map[string]any{"name": "invoke_subagent", "arguments": `{"role":"Performance Engineer","prompt":"Benchmark token capture latency"}`}},
					},
				},
				map[string]any{
					"role":         "tool",
					"tool_call_id": "call_cdx_maml_2",
					"content":      `{"status":"benchmark_pass","latency_ms":1.2}`,
				},
			)
		case 3:
			if !strings.Contains(bodyStr, "apply_diff") {
				t.Fatalf("turn 3 expected apply_diff: %s", bodyStr)
			}
			conversationHistory = append(conversationHistory,
				map[string]any{
					"role": "assistant",
					"tool_calls": []map[string]any{
						{"id": "call_cdx_maml_3", "type": "function", "function": map[string]any{"name": "apply_diff", "arguments": `{"file_path":"pkg/tools/codex.go","diff":"@@ -1,3 +1,4 @@\n+// optimized codex dialect"}`}},
					},
				},
				map[string]any{
					"role":         "tool",
					"tool_call_id": "call_cdx_maml_3",
					"content":      `{"status":"patch_applied","file":"pkg/tools/codex.go"}`,
				},
			)
		case 4:
			if !strings.Contains(bodyStr, "exec_command") {
				t.Fatalf("turn 4 expected exec_command: %s", bodyStr)
			}
			conversationHistory = append(conversationHistory,
				map[string]any{
					"role": "assistant",
					"tool_calls": []map[string]any{
						{"id": "call_cdx_maml_4", "type": "function", "function": map[string]any{"name": "exec_command", "arguments": `{"cmd":"go test ./pkg/tools"}`}},
					},
				},
				map[string]any{
					"role":         "tool",
					"tool_call_id": "call_cdx_maml_4",
					"content":      "ok amux-accounts/pkg/tools 0.22s",
				},
			)
		case 5:
			if !strings.Contains(bodyStr, "Codex multi-agent workflow succeeded") {
				t.Fatalf("turn 5 expected summary: %s", bodyStr)
			}
		}
	}

	if backend.turnCount != 5 {
		t.Fatalf("expected exactly 5 turns in multi-loop, got %d", backend.turnCount)
	}
}

// ----------------------------------------------------------------------------
// 5. Subscription Isolation in Codex (Never use Claude or AGY subscriptions)
// ----------------------------------------------------------------------------
func TestCodex_SubscriptionIsolation(t *testing.T) {
	providerInvoked := false
	backend := &mockCodexBackend{
		id: "chatgpt:01",
		onTurn: func(turn int, req *types.ChatRequest) []types.StreamChunk {
			providerInvoked = true
			return []types.StreamChunk{
				{ID: "chatgpt:01", Content: "Codex turn processed via provider pool. Claude/AGY subscriptions untouched."},
				{ID: "chatgpt:01", Done: true, FinishReason: "stop"},
			}
		},
	}
	pool := router.NewAccountPoolRouter([]types.ProviderAdapter{backend})

	reqBody := map[string]any{
		"model":  "gpt-4o",
		"stream": false, // Non-streaming JSON mode test
		"tools":  codexToolsCatalog(),
		"messages": []map[string]any{
			{"role": "user", "content": "Verify subscription isolation for Codex."},
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
	if !strings.Contains(content, "Claude/AGY subscriptions untouched") {
		t.Fatalf("unexpected content: %s", content)
	}
}
