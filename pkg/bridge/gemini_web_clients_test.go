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
	"amux-accounts/pkg/tools"
	"amux-accounts/pkg/types"
)

// mockGeminiWebBackend simulates the GeminiWebAdapter ("gemini:web:01")
// returning text formatted through the webloop tool protocol (<tool_call>).
// It wraps output with tools.MaybeWrapWebStream just like the real GeminiWebAdapter.
type mockGeminiWebBackend struct {
	mu           sync.Mutex
	turnCount    int
	onTurn       func(turn int, req *types.ChatRequest) string
	recordedReqs []*types.ChatRequest
}

func (m *mockGeminiWebBackend) ID() string    { return "gemini:web:01" }
func (m *mockGeminiWebBackend) Priority() int { return 1 }
func (m *mockGeminiWebBackend) SendMessageStream(ctx context.Context, req *types.ChatRequest) (<-chan types.StreamChunk, error) {
	m.mu.Lock()
	m.turnCount++
	turn := m.turnCount
	m.recordedReqs = append(m.recordedReqs, req)
	text := m.onTurn(turn, req)
	m.mu.Unlock()

	rawChan := make(chan types.StreamChunk, 2)
	rawChan <- types.StreamChunk{ID: "gemini:web:01", Content: text}
	rawChan <- types.StreamChunk{ID: "gemini:web:01", Done: true}
	close(rawChan)

	// In production, GeminiWebAdapter does:
	// return tools.MaybeWrapWebStream(a.AdapterID, req, out), nil
	return tools.MaybeWrapWebStream("gemini:web:01", req, rawChan), nil
}

// ============================================================================
// 1. CLAUDE CODE (POST /v1/messages) with gemini:web:01
// ============================================================================

func TestGeminiWeb_Claude_AllLoops(t *testing.T) {
	backend := &mockGeminiWebBackend{
		onTurn: func(turn int, req *types.ChatRequest) string {
			switch turn {
			case 1:
				// Single tool
				return `<tool_call>
{"name":"Bash","arguments":{"command":"uname -m"}}
</tool_call>`
			case 2:
				// Multi tool (parallel)
				return `<tool_call>
{"name":"Read","arguments":{"file_path":"README.md"}}
</tool_call>
<tool_call>
{"name":"Bash","arguments":{"command":"git branch --show-current"}}
</tool_call>`
			case 3:
				// Agent subagent
				return `<tool_call>
{"name":"invoke_subagent","arguments":{"role":"Research Analyst","prompt":"Analyze token compression architecture"}}
</tool_call>`
			default:
				// Final response
				return "Claude Code turn completed successfully via gemini:web:01."
			}
		},
	}
	pool := router.NewAccountPoolRouter([]types.ProviderAdapter{backend})

	claudeTools := []map[string]any{
		{
			"name":        "Bash",
			"description": "Run bash command",
			"input_schema": map[string]any{
				"type":       "object",
				"properties": map[string]any{"command": map[string]any{"type": "string"}},
				"required":   []string{"command"},
			},
		},
		{
			"name":        "Read",
			"description": "Read file",
			"input_schema": map[string]any{
				"type":       "object",
				"properties": map[string]any{"file_path": map[string]any{"type": "string"}},
				"required":   []string{"file_path"},
			},
		},
		{
			"name":        "invoke_subagent",
			"description": "Invoke subagent",
			"input_schema": map[string]any{
				"type":       "object",
				"properties": map[string]any{"role": map[string]any{"type": "string"}, "prompt": map[string]any{"type": "string"}},
				"required":   []string{"role", "prompt"},
			},
		},
	}

	// Turn 1: Single tool call (Bash)
	reqBody1 := map[string]any{
		"model":  "claude-3-7-sonnet-20250219",
		"stream": true,
		"tools":  claudeTools,
		"messages": []map[string]any{
			{"role": "user", "content": "Check architecture."},
		},
	}
	b1, _ := json.Marshal(reqBody1)
	req1 := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(b1))
	req1.Header.Set("X-Provider", "gemini:web:01")
	w1 := httptest.NewRecorder()
	bridge.HandleClaudeMessages(w1, req1, pool, b1)

	if w1.Code != http.StatusOK {
		t.Fatalf("claude turn 1 failed: %d %s", w1.Code, w1.Body.String())
	}
	resp1 := w1.Body.String()
	if !strings.Contains(resp1, "tool_use") || !strings.Contains(resp1, "Bash") {
		t.Fatalf("claude turn 1 expected tool_use: %s", resp1)
	}

	// Turn 2: Multi-tool (Read + Bash)
	reqBody2 := map[string]any{
		"model":  "claude-3-7-sonnet-20250219",
		"stream": true,
		"tools":  claudeTools,
		"messages": []map[string]any{
			{"role": "user", "content": "Check architecture."},
			{
				"role": "assistant",
				"content": []map[string]any{
					{"type": "tool_use", "id": "call_bash_1", "name": "Bash", "input": map[string]any{"command": "uname -m"}},
				},
			},
			{
				"role": "user",
				"content": []map[string]any{
					{"type": "tool_result", "tool_use_id": "call_bash_1", "content": "arm64"},
				},
			},
		},
	}
	b2, _ := json.Marshal(reqBody2)
	req2 := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(b2))
	req2.Header.Set("X-Provider", "gemini:web:01")
	w2 := httptest.NewRecorder()
	bridge.HandleClaudeMessages(w2, req2, pool, b2)

	if w2.Code != http.StatusOK {
		t.Fatalf("claude turn 2 failed: %d %s", w2.Code, w2.Body.String())
	}
	resp2 := w2.Body.String()
	if !strings.Contains(resp2, "Read") || !strings.Contains(resp2, "Bash") {
		t.Fatalf("claude turn 2 expected multi-tool: %s", resp2)
	}

	// Turn 3: Agent subagent delegation
	reqBody3 := map[string]any{
		"model":  "claude-3-7-sonnet-20250219",
		"stream": true,
		"tools":  claudeTools,
		"messages": []map[string]any{
			{"role": "user", "content": "Delegate research to subagent."},
		},
	}
	b3, _ := json.Marshal(reqBody3)
	req3 := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(b3))
	req3.Header.Set("X-Provider", "gemini:web:01")
	w3 := httptest.NewRecorder()
	bridge.HandleClaudeMessages(w3, req3, pool, b3)

	if w3.Code != http.StatusOK {
		t.Fatalf("claude turn 3 failed: %d %s", w3.Code, w3.Body.String())
	}
	resp3 := w3.Body.String()
	if !strings.Contains(resp3, "invoke_subagent") || !strings.Contains(resp3, "Research Analyst") {
		t.Fatalf("claude turn 3 expected subagent: %s", resp3)
	}
}

// ============================================================================
// 2. CURSOR (/v1/chat/completions) with gemini:web:01
// ============================================================================

func TestGeminiWeb_Cursor_AllLoops(t *testing.T) {
	backend := &mockGeminiWebBackend{
		onTurn: func(turn int, req *types.ChatRequest) string {
			switch turn {
			case 1:
				// Single tool
				return `<tool_call>
{"name":"run_terminal_command","arguments":{"command":"sw_vers"}}
</tool_call>`
			case 2:
				// Multi tool
				return `<tool_call>
{"name":"read_file","arguments":{"path":"main.go"}}
</tool_call>
<tool_call>
{"name":"grep_search","arguments":{"query":"Run"}}
</tool_call>`
			case 3:
				// Subagent
				return `<tool_call>
{"name":"invoke_subagent","arguments":{"role":"Cursor Architect","prompt":"Audit memory overhead"}}
</tool_call>`
			default:
				return "Cursor loop finished cleanly on gemini:web:01."
			}
		},
	}
	pool := router.NewAccountPoolRouter([]types.ProviderAdapter{backend})

	// Turn 1: Single tool (run_terminal_command)
	reqBody1 := map[string]any{
		"model":  "gpt-4o",
		"stream": true,
		"tools":  cursorToolsCatalog(),
		"messages": []map[string]any{
			{"role": "user", "content": "Check macOS version."},
		},
	}
	b1, _ := json.Marshal(reqBody1)
	req1 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(b1))
	req1.Header.Set("X-Provider", "gemini:web:01")
	w1 := httptest.NewRecorder()
	bridge.HandleChatCompletions(w1, req1, pool)

	if w1.Code != http.StatusOK {
		t.Fatalf("cursor turn 1 failed: %d %s", w1.Code, w1.Body.String())
	}
	resp1 := w1.Body.String()
	if !strings.Contains(resp1, "run_terminal_command") {
		t.Fatalf("cursor turn 1 expected run_terminal_command: %s", resp1)
	}

	// Turn 2: Multi-tool parallel calls
	reqBody2 := map[string]any{
		"model":  "gpt-4o",
		"stream": true,
		"tools":  cursorToolsCatalog(),
		"messages": []map[string]any{
			{"role": "user", "content": "Check macOS version."},
			{
				"role": "assistant",
				"tool_calls": []map[string]any{
					{"id": "call_1", "type": "function", "function": map[string]any{"name": "run_terminal_command", "arguments": `{"command":"sw_vers"}`}},
				},
			},
			{"role": "tool", "tool_call_id": "call_1", "content": "macOS 15.3"},
		},
	}
	b2, _ := json.Marshal(reqBody2)
	req2 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(b2))
	req2.Header.Set("X-Provider", "gemini:web:01")
	w2 := httptest.NewRecorder()
	bridge.HandleChatCompletions(w2, req2, pool)

	if w2.Code != http.StatusOK {
		t.Fatalf("cursor turn 2 failed: %d %s", w2.Code, w2.Body.String())
	}
	resp2 := w2.Body.String()
	if !strings.Contains(resp2, "read_file") || !strings.Contains(resp2, "grep_search") {
		t.Fatalf("cursor turn 2 expected multi-tool: %s", resp2)
	}

	// Turn 3: Subagent call
	reqBody3 := map[string]any{
		"model":  "gpt-4o",
		"stream": true,
		"tools":  cursorToolsCatalog(),
		"messages": []map[string]any{
			{"role": "user", "content": "Delegate audit to subagent."},
		},
	}
	b3, _ := json.Marshal(reqBody3)
	req3 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(b3))
	req3.Header.Set("X-Provider", "gemini:web:01")
	w3 := httptest.NewRecorder()
	bridge.HandleChatCompletions(w3, req3, pool)

	if w3.Code != http.StatusOK {
		t.Fatalf("cursor turn 3 failed: %d %s", w3.Code, w3.Body.String())
	}
	resp3 := w3.Body.String()
	if !strings.Contains(resp3, "invoke_subagent") || !strings.Contains(resp3, "Cursor Architect") {
		t.Fatalf("cursor turn 3 expected subagent: %s", resp3)
	}
}

// ============================================================================
// 3. ANTIGRAVITY (AGY) (:streamGenerateContent) with gemini:web:01
// ============================================================================

func TestGeminiWeb_Antigravity_AllLoops(t *testing.T) {
	backend := &mockGeminiWebBackend{
		onTurn: func(turn int, req *types.ChatRequest) string {
			switch turn {
			case 1:
				// Single tool in AGY
				return `<tool_call>
{"name":"run_command","arguments":{"CommandLine":"pwd"}}
</tool_call>`
			case 2:
				// Multi tool parallel in AGY
				return `<tool_call>
{"name":"view_file","arguments":{"AbsolutePath":"/Users/ninh.le/Documents/apps/amux/main.go"}}
</tool_call>
<tool_call>
{"name":"run_command","arguments":{"CommandLine":"ls -la"}}
</tool_call>`
			case 3:
				// Subagent delegation in AGY
				return `<tool_call>
{"name":"invoke_subagent","arguments":{"Role":"Gemini Researcher","Prompt":"Analyze Gemini Web cookies"}}
</tool_call>`
			default:
				return "Antigravity AGY loop fully completed on gemini:web:01."
			}
		},
	}
	pool := router.NewAccountPoolRouter([]types.ProviderAdapter{backend})

	agyTools := []map[string]any{
		{
			"functionDeclarations": []map[string]any{
				{
					"name":        "run_command",
					"description": "Run shell command",
					"parameters": map[string]any{
						"type":       "object",
						"properties": map[string]any{"CommandLine": map[string]any{"type": "string"}},
						"required":   []string{"CommandLine"},
					},
				},
				{
					"name":        "view_file",
					"description": "View file contents",
					"parameters": map[string]any{
						"type":       "object",
						"properties": map[string]any{"AbsolutePath": map[string]any{"type": "string"}},
						"required":   []string{"AbsolutePath"},
					},
				},
				{
					"name":        "invoke_subagent",
					"description": "Invoke subagent",
					"parameters": map[string]any{
						"type":       "object",
						"properties": map[string]any{"Role": map[string]any{"type": "string"}, "Prompt": map[string]any{"type": "string"}},
						"required":   []string{"Role", "Prompt"},
					},
				},
			},
		},
	}

	// Turn 1: AGY single tool (run_command)
	reqBody1 := map[string]any{
		"contents": []map[string]any{
			{"role": "user", "parts": []map[string]any{{"text": "Print current working directory."}}},
		},
		"tools": agyTools,
	}
	b1, _ := json.Marshal(reqBody1)
	req1 := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-flash:streamGenerateContent", bytes.NewReader(b1))
	req1.Header.Set("X-Provider", "gemini:web:01")
	w1 := httptest.NewRecorder()
	bridge.HandleGeminiGenerateContent(w1, req1, pool)

	if w1.Code != http.StatusOK {
		t.Fatalf("agy turn 1 failed: %d %s", w1.Code, w1.Body.String())
	}
	resp1 := w1.Body.String()
	if !strings.Contains(resp1, "functionCall") || !strings.Contains(resp1, "run_command") {
		t.Fatalf("agy turn 1 expected functionCall: %s", resp1)
	}

	// Turn 2: AGY multi-tool (view_file + run_command)
	reqBody2 := map[string]any{
		"contents": []map[string]any{
			{"role": "user", "parts": []map[string]any{{"text": "Print current working directory."}}},
			{
				"role": "model",
				"parts": []map[string]any{
					{"functionCall": map[string]any{"name": "run_command", "args": map[string]any{"CommandLine": "pwd"}}},
				},
			},
			{
				"role": "user",
				"parts": []map[string]any{
					{"functionResponse": map[string]any{"name": "run_command", "response": map[string]any{"output": "/Users/ninh.le/Documents/apps/amux\n"}}},
				},
			},
		},
		"tools": agyTools,
	}
	b2, _ := json.Marshal(reqBody2)
	req2 := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-flash:streamGenerateContent", bytes.NewReader(b2))
	req2.Header.Set("X-Provider", "gemini:web:01")
	w2 := httptest.NewRecorder()
	bridge.HandleGeminiGenerateContent(w2, req2, pool)

	if w2.Code != http.StatusOK {
		t.Fatalf("agy turn 2 failed: %d %s", w2.Code, w2.Body.String())
	}
	resp2 := w2.Body.String()
	if !strings.Contains(resp2, "view_file") || !strings.Contains(resp2, "run_command") {
		t.Fatalf("agy turn 2 expected multi-tool functionCalls: %s", resp2)
	}

	// Turn 3: AGY subagent
	reqBody3 := map[string]any{
		"contents": []map[string]any{
			{"role": "user", "parts": []map[string]any{{"text": "Launch research subagent."}}},
		},
		"tools": agyTools,
	}
	b3, _ := json.Marshal(reqBody3)
	req3 := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-flash:streamGenerateContent", bytes.NewReader(b3))
	req3.Header.Set("X-Provider", "gemini:web:01")
	w3 := httptest.NewRecorder()
	bridge.HandleGeminiGenerateContent(w3, req3, pool)

	if w3.Code != http.StatusOK {
		t.Fatalf("agy turn 3 failed: %d %s", w3.Code, w3.Body.String())
	}
	resp3 := w3.Body.String()
	if !strings.Contains(resp3, "invoke_subagent") || !strings.Contains(resp3, "Gemini Researcher") {
		t.Fatalf("agy turn 3 expected subagent: %s", resp3)
	}
}

// ============================================================================
// 4. CODEX (/v1/chat/completions) with gemini:web:01
// ============================================================================

func TestGeminiWeb_Codex_AllLoops(t *testing.T) {
	backend := &mockGeminiWebBackend{
		onTurn: func(turn int, req *types.ChatRequest) string {
			switch turn {
			case 1:
				// Single tool
				return `<tool_call>
{"name":"exec_command","arguments":{"cmd":"git status"}}
</tool_call>`
			case 2:
				// Multi tool
				return `<tool_call>
{"name":"read_file","arguments":{"file_path":"go.mod"}}
</tool_call>
<tool_call>
{"name":"grep_search","arguments":{"pattern":"gemini_web"}}
</tool_call>`
			case 3:
				// Subagent
				return `<tool_call>
{"name":"invoke_subagent","arguments":{"role":"Codex Automator","prompt":"Run automated integration check"}}
</tool_call>`
			default:
				return "Codex loop completed on gemini:web:01."
			}
		},
	}
	pool := router.NewAccountPoolRouter([]types.ProviderAdapter{backend})

	// Turn 1: Codex single tool
	reqBody1 := map[string]any{
		"model":  "gpt-4o",
		"stream": true,
		"tools":  codexToolsCatalog(),
		"messages": []map[string]any{
			{"role": "user", "content": "Execute git status."},
		},
	}
	b1, _ := json.Marshal(reqBody1)
	req1 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(b1))
	req1.Header.Set("X-Provider", "gemini:web:01")
	w1 := httptest.NewRecorder()
	bridge.HandleChatCompletions(w1, req1, pool)

	if w1.Code != http.StatusOK {
		t.Fatalf("codex turn 1 failed: %d %s", w1.Code, w1.Body.String())
	}
	resp1 := w1.Body.String()
	if !strings.Contains(resp1, "exec_command") {
		t.Fatalf("codex turn 1 expected exec_command: %s", resp1)
	}

	// Turn 2: Codex multi-tool
	reqBody2 := map[string]any{
		"model":  "gpt-4o",
		"stream": true,
		"tools":  codexToolsCatalog(),
		"messages": []map[string]any{
			{"role": "user", "content": "Execute git status."},
			{
				"role": "assistant",
				"tool_calls": []map[string]any{
					{"id": "c1", "type": "function", "function": map[string]any{"name": "exec_command", "arguments": `{"cmd":"git status"}`}},
				},
			},
			{"role": "tool", "tool_call_id": "c1", "content": "working tree clean"},
		},
	}
	b2, _ := json.Marshal(reqBody2)
	req2 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(b2))
	req2.Header.Set("X-Provider", "gemini:web:01")
	w2 := httptest.NewRecorder()
	bridge.HandleChatCompletions(w2, req2, pool)

	if w2.Code != http.StatusOK {
		t.Fatalf("codex turn 2 failed: %d %s", w2.Code, w2.Body.String())
	}
	resp2 := w2.Body.String()
	if !strings.Contains(resp2, "read_file") || !strings.Contains(resp2, "grep_search") {
		t.Fatalf("codex turn 2 expected multi-tool: %s", resp2)
	}

	// Turn 3: Codex subagent
	reqBody3 := map[string]any{
		"model":  "gpt-4o",
		"stream": true,
		"tools":  codexToolsCatalog(),
		"messages": []map[string]any{
			{"role": "user", "content": "Delegate integration check to subagent."},
		},
	}
	b3, _ := json.Marshal(reqBody3)
	req3 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(b3))
	req3.Header.Set("X-Provider", "gemini:web:01")
	w3 := httptest.NewRecorder()
	bridge.HandleChatCompletions(w3, req3, pool)

	if w3.Code != http.StatusOK {
		t.Fatalf("codex turn 3 failed: %d %s", w3.Code, w3.Body.String())
	}
	resp3 := w3.Body.String()
	if !strings.Contains(resp3, "invoke_subagent") || !strings.Contains(resp3, "Codex Automator") {
		t.Fatalf("codex turn 3 expected subagent: %s", resp3)
	}
}

// ============================================================================
// 5. SUBSCRIPTION ISOLATION VERIFICATION
// ============================================================================

func TestGeminiWeb_SubscriptionIsolation(t *testing.T) {
	geminiWebCalled := false
	backend := &mockGeminiWebBackend{
		onTurn: func(turn int, req *types.ChatRequest) string {
			geminiWebCalled = true
			return "Confirmed: Served entirely by gemini:web:01. Zero Claude/AGY subscriptions."
		},
	}
	pool := router.NewAccountPoolRouter([]types.ProviderAdapter{backend})

	// Test each client endpoint with X-Provider: gemini:web:01
	clients := []struct {
		name    string
		handler func(w http.ResponseWriter, r *http.Request, rawBody []byte)
		path    string
		body    map[string]any
	}{
		{
			name: "claude",
			handler: func(w http.ResponseWriter, r *http.Request, rawBody []byte) {
				bridge.HandleClaudeMessages(w, r, pool, rawBody)
			},
			path: "/v1/messages",
			body: map[string]any{
				"model":    "claude-3-7-sonnet-20250219",
				"messages": []map[string]any{{"role": "user", "content": "ping"}},
			},
		},
		{
			name: "cursor",
			handler: func(w http.ResponseWriter, r *http.Request, rawBody []byte) {
				bridge.HandleChatCompletions(w, r, pool)
			},
			path: "/v1/chat/completions",
			body: map[string]any{
				"model":    "gpt-4o",
				"messages": []map[string]any{{"role": "user", "content": "ping"}},
			},
		},
		{
			name: "antigravity",
			handler: func(w http.ResponseWriter, r *http.Request, rawBody []byte) {
				bridge.HandleGeminiGenerateContent(w, r, pool)
			},
			path: "/v1beta/models/gemini-2.5-flash:generateContent",
			body: map[string]any{
				"contents": []map[string]any{{"role": "user", "parts": []map[string]any{{"text": "ping"}}}},
			},
		},
		{
			name: "codex",
			handler: func(w http.ResponseWriter, r *http.Request, rawBody []byte) {
				bridge.HandleChatCompletions(w, r, pool)
			},
			path: "/v1/chat/completions",
			body: map[string]any{
				"model":    "gpt-4o",
				"messages": []map[string]any{{"role": "user", "content": "ping"}},
			},
		},
	}

	for _, tc := range clients {
		t.Run(tc.name, func(t *testing.T) {
			geminiWebCalled = false
			b, _ := json.Marshal(tc.body)
			req := httptest.NewRequest(http.MethodPost, tc.path, bytes.NewReader(b))
			req.Header.Set("X-Provider", "gemini:web:01")
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			tc.handler(w, req, b)
			if w.Code != http.StatusOK {
				t.Fatalf("%s failed with code %d: %s", tc.name, w.Code, w.Body.String())
			}
			if !geminiWebCalled {
				t.Fatalf("%s did not route to gemini:web:01", tc.name)
			}
			if !strings.Contains(w.Body.String(), "gemini:web:01") {
				t.Fatalf("%s unexpected response: %s", tc.name, w.Body.String())
			}
		})
	}
}

// ============================================================================
// 6. MULTI-AGENT MULTI-LOOP 5-TURN ITERATIVE WORKFLOWS ON gemini:web:01
// ============================================================================

func TestGeminiWeb_MultiAgentMultiLoop_Claude(t *testing.T) {
	backend := &mockGeminiWebBackend{
		onTurn: func(turn int, req *types.ChatRequest) string {
			switch turn {
			case 1:
				return `<tool_call>
{"name":"invoke_subagent","arguments":{"role":"Architecture Auditor","prompt":"Check amux gateway"}}
</tool_call>`
			case 2:
				return `<tool_call>
{"name":"invoke_subagent","arguments":{"role":"Security Auditor","prompt":"Check tunnel security"}}
</tool_call>`
			case 3:
				return `<tool_call>
{"name":"Read","arguments":{"file_path":"pkg/proxy/tunnel.go"}}
</tool_call>`
			case 4:
				return `<tool_call>
{"name":"Bash","arguments":{"command":"go test ./pkg/proxy"}}
</tool_call>`
			default:
				return "Claude multi-agent multi-loop 5-turn workflow complete on gemini:web:01."
			}
		},
	}
	pool := router.NewAccountPoolRouter([]types.ProviderAdapter{backend})

	claudeTools := []map[string]any{
		{"name": "Bash", "input_schema": map[string]any{"type": "object", "properties": map[string]any{"command": map[string]any{"type": "string"}}}},
		{"name": "Read", "input_schema": map[string]any{"type": "object", "properties": map[string]any{"file_path": map[string]any{"type": "string"}}}},
		{"name": "invoke_subagent", "input_schema": map[string]any{"type": "object", "properties": map[string]any{"role": map[string]any{"type": "string"}, "prompt": map[string]any{"type": "string"}}}},
	}

	var messages []map[string]any
	messages = append(messages, map[string]any{"role": "user", "content": "Execute multi-agent audit on gemini:web:01."})

	for turn := 1; turn <= 5; turn++ {
		reqBody := map[string]any{
			"model":    "claude-3-7-sonnet-20250219",
			"stream":   true,
			"tools":    claudeTools,
			"messages": messages,
		}
		b, _ := json.Marshal(reqBody)
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(b))
		req.Header.Set("X-Provider", "gemini:web:01")
		w := httptest.NewRecorder()
		bridge.HandleClaudeMessages(w, req, pool, b)

		if w.Code != http.StatusOK {
			t.Fatalf("claude turn %d failed: %d %s", turn, w.Code, w.Body.String())
		}
		bodyStr := w.Body.String()

		switch turn {
		case 1:
			if !strings.Contains(bodyStr, "Architecture Auditor") {
				t.Fatalf("turn 1 expected Architecture Auditor: %s", bodyStr)
			}
			messages = append(messages,
				map[string]any{
					"role": "assistant",
					"content": []map[string]any{
						{"type": "tool_use", "id": "call_1", "name": "invoke_subagent", "input": map[string]any{"role": "Architecture Auditor", "prompt": "Check amux gateway"}},
					},
				},
				map[string]any{
					"role": "user",
					"content": []map[string]any{
						{"type": "tool_result", "tool_use_id": "call_1", "content": "Architecture approved."},
					},
				},
			)
		case 2:
			if !strings.Contains(bodyStr, "Security Auditor") {
				t.Fatalf("turn 2 expected Security Auditor: %s", bodyStr)
			}
			messages = append(messages,
				map[string]any{
					"role": "assistant",
					"content": []map[string]any{
						{"type": "tool_use", "id": "call_2", "name": "invoke_subagent", "input": map[string]any{"role": "Security Auditor", "prompt": "Check tunnel security"}},
					},
				},
				map[string]any{
					"role": "user",
					"content": []map[string]any{
						{"type": "tool_result", "tool_use_id": "call_2", "content": "Security verified."},
					},
				},
			)
		case 3:
			if !strings.Contains(bodyStr, "Read") {
				t.Fatalf("turn 3 expected Read: %s", bodyStr)
			}
			messages = append(messages,
				map[string]any{
					"role": "assistant",
					"content": []map[string]any{
						{"type": "tool_use", "id": "call_3", "name": "Read", "input": map[string]any{"file_path": "pkg/proxy/tunnel.go"}},
					},
				},
				map[string]any{
					"role": "user",
					"content": []map[string]any{
						{"type": "tool_result", "tool_use_id": "call_3", "content": "package proxy\nfunc handleConnectTunnel..."},
					},
				},
			)
		case 4:
			if !strings.Contains(bodyStr, "Bash") {
				t.Fatalf("turn 4 expected Bash: %s", bodyStr)
			}
			messages = append(messages,
				map[string]any{
					"role": "assistant",
					"content": []map[string]any{
						{"type": "tool_use", "id": "call_4", "name": "Bash", "input": map[string]any{"command": "go test ./pkg/proxy"}},
					},
				},
				map[string]any{
					"role": "user",
					"content": []map[string]any{
						{"type": "tool_result", "tool_use_id": "call_4", "content": "PASS ok pkg/proxy"},
					},
				},
			)
		case 5:
			if !strings.Contains(bodyStr, "Claude multi-agent multi-loop") {
				t.Fatalf("turn 5 expected completion: %s", bodyStr)
			}
		}
	}
}

func TestGeminiWeb_MultiAgentMultiLoop_Antigravity(t *testing.T) {
	backend := &mockGeminiWebBackend{
		onTurn: func(turn int, req *types.ChatRequest) string {
			switch turn {
			case 1:
				return `<tool_call>
{"name":"invoke_subagent","arguments":{"Role":"AGY Auditor","Prompt":"Inspect gemini bridge"}}
</tool_call>`
			case 2:
				return `<tool_call>
{"name":"view_file","arguments":{"AbsolutePath":"pkg/bridge/gemini.go"}}
</tool_call>`
			case 3:
				return `<tool_call>
{"name":"run_command","arguments":{"CommandLine":"go test ./pkg/bridge"}}
</tool_call>`
			default:
				return "Antigravity AGY multi-turn loop complete on gemini:web:01."
			}
		},
	}
	pool := router.NewAccountPoolRouter([]types.ProviderAdapter{backend})

	agyTools := []map[string]any{
		{
			"functionDeclarations": []map[string]any{
				{"name": "run_command", "parameters": map[string]any{"type": "object", "properties": map[string]any{"CommandLine": map[string]any{"type": "string"}}}},
				{"name": "view_file", "parameters": map[string]any{"type": "object", "properties": map[string]any{"AbsolutePath": map[string]any{"type": "string"}}}},
				{"name": "invoke_subagent", "parameters": map[string]any{"type": "object", "properties": map[string]any{"Role": map[string]any{"type": "string"}, "Prompt": map[string]any{"type": "string"}}}},
			},
		},
	}

	var contents []map[string]any
	contents = append(contents, map[string]any{
		"role":  "user",
		"parts": []map[string]any{{"text": "Run multi-turn verification on gemini:web:01."}},
	})

	for turn := 1; turn <= 4; turn++ {
		reqBody := map[string]any{
			"contents": contents,
			"tools":    agyTools,
		}
		b, _ := json.Marshal(reqBody)
		req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-pro:streamGenerateContent", bytes.NewReader(b))
		req.Header.Set("X-Provider", "gemini:web:01")
		w := httptest.NewRecorder()
		bridge.HandleGeminiGenerateContent(w, req, pool)

		if w.Code != http.StatusOK {
			t.Fatalf("agy turn %d failed: %d %s", turn, w.Code, w.Body.String())
		}
		bodyStr := w.Body.String()

		switch turn {
		case 1:
			if !strings.Contains(bodyStr, "AGY Auditor") {
				t.Fatalf("agy turn 1 expected AGY Auditor: %s", bodyStr)
			}
			contents = append(contents,
				map[string]any{
					"role": "model",
					"parts": []map[string]any{
						{"functionCall": map[string]any{"name": "invoke_subagent", "args": map[string]any{"Role": "AGY Auditor", "Prompt": "Inspect gemini bridge"}}},
					},
				},
				map[string]any{
					"role": "user",
					"parts": []map[string]any{
						{"functionResponse": map[string]any{"name": "invoke_subagent", "response": map[string]any{"result": "Audit clean."}}},
					},
				},
			)
		case 2:
			if !strings.Contains(bodyStr, "view_file") {
				t.Fatalf("agy turn 2 expected view_file: %s", bodyStr)
			}
			contents = append(contents,
				map[string]any{
					"role": "model",
					"parts": []map[string]any{
						{"functionCall": map[string]any{"name": "view_file", "args": map[string]any{"AbsolutePath": "pkg/bridge/gemini.go"}}},
					},
				},
				map[string]any{
					"role": "user",
					"parts": []map[string]any{
						{"functionResponse": map[string]any{"name": "view_file", "response": map[string]any{"content": "package bridge"}}},
					},
				},
			)
		case 3:
			if !strings.Contains(bodyStr, "run_command") {
				t.Fatalf("agy turn 3 expected run_command: %s", bodyStr)
			}
			contents = append(contents,
				map[string]any{
					"role": "model",
					"parts": []map[string]any{
						{"functionCall": map[string]any{"name": "run_command", "args": map[string]any{"CommandLine": "go test ./pkg/bridge"}}},
					},
				},
				map[string]any{
					"role": "user",
					"parts": []map[string]any{
						{"functionResponse": map[string]any{"name": "run_command", "response": map[string]any{"output": "PASS"}}},
					},
				},
			)
		case 4:
			if !strings.Contains(bodyStr, "Antigravity AGY multi-turn loop complete") {
				t.Fatalf("agy turn 4 expected final output: %s", bodyStr)
			}
		}
	}
}

// ============================================================================
// 7. ULTIMATE TEST: MULTI-TOOLS + MULTI-SUBAGENTS + MULTI-AGENTS + MULTI-LOOP
// ============================================================================

// Turn 1: Master Agent dispatches PARALLEL MULTI-SUBAGENTS (Researcher + Auditor)
// Turn 2: Subagents report -> Master Agent calls PARALLEL MULTI-TOOLS (Read + Read + Bash)
// Turn 3: Tools report -> Master Agent calls HYBRID (Subagent + Tool together)
// Turn 4: Tool reports -> Master Agent calls VERIFICATION TOOL (Bash)
// Turn 5: Final conclusion response
func TestGeminiWeb_Ultimate_MultiTools_MultiAgent_MultiSubAgent_MultiLoop(t *testing.T) {
	backend := &mockGeminiWebBackend{
		onTurn: func(turn int, req *types.ChatRequest) string {
			switch turn {
			case 1:
				// PARALLEL MULTI-SUBAGENTS: 2 subagents dispatched at the same time
				return `<tool_call>
{"name":"invoke_subagent","arguments":{"role":"Performance Analyst","prompt":"Analyze token caching latency"}}
</tool_call>
<tool_call>
{"name":"invoke_subagent","arguments":{"role":"Security Auditor","prompt":"Check secret redaction coverage"}}
</tool_call>`
			case 2:
				// PARALLEL MULTI-TOOLS: 3 regular tools executed simultaneously
				return `<tool_call>
{"name":"Read","arguments":{"file_path":"pkg/privacy/redact.go"}}
</tool_call>
<tool_call>
{"name":"Read","arguments":{"file_path":"pkg/tools/webloop.go"}}
</tool_call>
<tool_call>
{"name":"Bash","arguments":{"command":"go test ./pkg/privacy"}}
</tool_call>`
			case 3:
				// HYBRID MULTI-CALL: Subagent AND tool dispatched in the same turn
				return `<tool_call>
{"name":"invoke_subagent","arguments":{"role":"Refactor Specialist","prompt":"Synthesize patch"}}
</tool_call>
<tool_call>
{"name":"Bash","arguments":{"command":"git diff"}}
</tool_call>`
			case 4:
				// Sequential verification loop
				return `<tool_call>
{"name":"Bash","arguments":{"command":"go test ./pkg/tools"}}
</tool_call>`
			default:
				// Final synthesis turn
				return "Ultimate verification complete: Multi-tools, Multi-agents, Multi-subagents, and Multi-loops all succeeded flawlessly on gemini:web:01."
			}
		},
	}
	pool := router.NewAccountPoolRouter([]types.ProviderAdapter{backend})

	claudeTools := []map[string]any{
		{"name": "Bash", "input_schema": map[string]any{"type": "object", "properties": map[string]any{"command": map[string]any{"type": "string"}}}},
		{"name": "Read", "input_schema": map[string]any{"type": "object", "properties": map[string]any{"file_path": map[string]any{"type": "string"}}}},
		{"name": "invoke_subagent", "input_schema": map[string]any{"type": "object", "properties": map[string]any{"role": map[string]any{"type": "string"}, "prompt": map[string]any{"type": "string"}}}},
	}

	var messages []map[string]any
	messages = append(messages, map[string]any{
		"role":    "user",
		"content": "Run full multi-agent, multi-subagent, multi-tool audit on gemini:web:01.",
	})

	for turn := 1; turn <= 5; turn++ {
		reqBody := map[string]any{
			"model":    "claude-3-7-sonnet-20250219",
			"stream":   true,
			"tools":    claudeTools,
			"messages": messages,
		}
		b, _ := json.Marshal(reqBody)
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(b))
		req.Header.Set("X-Provider", "gemini:web:01")
		w := httptest.NewRecorder()
		bridge.HandleClaudeMessages(w, req, pool, b)

		if w.Code != http.StatusOK {
			t.Fatalf("turn %d failed: %d %s", turn, w.Code, w.Body.String())
		}
		bodyStr := w.Body.String()

		switch turn {
		case 1:
			// Verify BOTH subagents were parsed and returned in the same turn
			if !strings.Contains(bodyStr, "Performance Analyst") || !strings.Contains(bodyStr, "Security Auditor") {
				t.Fatalf("turn 1 expected both parallel subagents: %s", bodyStr)
			}
			messages = append(messages,
				map[string]any{
					"role": "assistant",
					"content": []map[string]any{
						{"type": "tool_use", "id": "call_sub_1", "name": "invoke_subagent", "input": map[string]any{"role": "Performance Analyst", "prompt": "Analyze token caching latency"}},
						{"type": "tool_use", "id": "call_sub_2", "name": "invoke_subagent", "input": map[string]any{"role": "Security Auditor", "prompt": "Check secret redaction coverage"}},
					},
				},
				map[string]any{
					"role": "user",
					"content": []map[string]any{
						{"type": "tool_result", "tool_use_id": "call_sub_1", "content": `{"status":"perf_ok","cache_hit_rate":0.92}`},
						{"type": "tool_result", "tool_use_id": "call_sub_2", "content": `{"status":"sec_ok","redacted_patterns":18}`},
					},
				},
			)
		case 2:
			// Verify ALL 3 parallel tools were returned in the same turn
			if !strings.Contains(bodyStr, "pkg/privacy/redact.go") || !strings.Contains(bodyStr, "pkg/tools/webloop.go") || !strings.Contains(bodyStr, "go test ./pkg/privacy") {
				t.Fatalf("turn 2 expected all 3 parallel tools: %s", bodyStr)
			}
			messages = append(messages,
				map[string]any{
					"role": "assistant",
					"content": []map[string]any{
						{"type": "tool_use", "id": "call_t_1", "name": "Read", "input": map[string]any{"file_path": "pkg/privacy/redact.go"}},
						{"type": "tool_use", "id": "call_t_2", "name": "Read", "input": map[string]any{"file_path": "pkg/tools/webloop.go"}},
						{"type": "tool_use", "id": "call_t_3", "name": "Bash", "input": map[string]any{"command": "go test ./pkg/privacy"}},
					},
				},
				map[string]any{
					"role": "user",
					"content": []map[string]any{
						{"type": "tool_result", "tool_use_id": "call_t_1", "content": "package privacy // 100 lines"},
						{"type": "tool_result", "tool_use_id": "call_t_2", "content": "package tools // webloop 700 lines"},
						{"type": "tool_result", "tool_use_id": "call_t_3", "content": "ok amux-accounts/pkg/privacy 0.12s"},
					},
				},
			)
		case 3:
			// Verify HYBRID subagent + tool in the same turn
			if !strings.Contains(bodyStr, "Refactor Specialist") || !strings.Contains(bodyStr, "git diff") {
				t.Fatalf("turn 3 expected hybrid subagent + tool: %s", bodyStr)
			}
			messages = append(messages,
				map[string]any{
					"role": "assistant",
					"content": []map[string]any{
						{"type": "tool_use", "id": "call_h_1", "name": "invoke_subagent", "input": map[string]any{"role": "Refactor Specialist", "prompt": "Synthesize patch"}},
						{"type": "tool_use", "id": "call_h_2", "name": "Bash", "input": map[string]any{"command": "git diff"}},
					},
				},
				map[string]any{
					"role": "user",
					"content": []map[string]any{
						{"type": "tool_result", "tool_use_id": "call_h_1", "content": "Refactoring verified."},
						{"type": "tool_result", "tool_use_id": "call_h_2", "content": "No uncommitted diffs."},
					},
				},
			)
		case 4:
			if !strings.Contains(bodyStr, "go test ./pkg/tools") {
				t.Fatalf("turn 4 expected verification tool: %s", bodyStr)
			}
			messages = append(messages,
				map[string]any{
					"role": "assistant",
					"content": []map[string]any{
						{"type": "tool_use", "id": "call_v_1", "name": "Bash", "input": map[string]any{"command": "go test ./pkg/tools"}},
					},
				},
				map[string]any{
					"role": "user",
					"content": []map[string]any{
						{"type": "tool_result", "tool_use_id": "call_v_1", "content": "ok amux-accounts/pkg/tools 0.35s"},
					},
				},
			)
		case 5:
			if !strings.Contains(bodyStr, "Ultimate verification complete") {
				t.Fatalf("turn 5 expected ultimate conclusion: %s", bodyStr)
			}
		}
	}
}

// Same Ultimate test on CURSOR wire format
func TestGeminiWeb_Ultimate_Cursor(t *testing.T) {
	backend := &mockGeminiWebBackend{
		onTurn: func(turn int, req *types.ChatRequest) string {
			switch turn {
			case 1:
				// Parallel multi subagents
				return `<tool_call>
{"name":"invoke_subagent","arguments":{"role":"Cursor Planner","prompt":"Plan refactor"}}
</tool_call>
<tool_call>
{"name":"invoke_subagent","arguments":{"role":"Cursor Auditor","prompt":"Audit security"}}
</tool_call>`
			case 2:
				// Parallel multi tools
				return `<tool_call>
{"name":"read_file","arguments":{"path":"main.go"}}
</tool_call>
<tool_call>
{"name":"grep_search","arguments":{"query":"HandleChatCompletions"}}
</tool_call>
<tool_call>
{"name":"run_terminal_command","arguments":{"command":"go test ./pkg/proxy"}}
</tool_call>`
			case 3:
				// Hybrid subagent + tool
				return `<tool_call>
{"name":"invoke_subagent","arguments":{"role":"Cursor Verifier","prompt":"Verify test suite"}}
</tool_call>
<tool_call>
{"name":"edit_file","arguments":{"path":"patch.go","code":"package main"}}
</tool_call>`
			case 4:
				return `<tool_call>
{"name":"run_terminal_command","arguments":{"command":"go version"}}
</tool_call>`
			default:
				return "Cursor ultimate multi-subagent multi-tool multi-loop passed on gemini:web:01."
			}
		},
	}
	pool := router.NewAccountPoolRouter([]types.ProviderAdapter{backend})

	var messages []map[string]any
	messages = append(messages, map[string]any{"role": "user", "content": "Execute ultimate Cursor multi-subagent workflow."})

	for turn := 1; turn <= 5; turn++ {
		reqBody := map[string]any{
			"model":    "gpt-4o",
			"stream":   true,
			"tools":    cursorToolsCatalog(),
			"messages": messages,
		}
		b, _ := json.Marshal(reqBody)
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(b))
		req.Header.Set("X-Provider", "gemini:web:01")
		w := httptest.NewRecorder()
		bridge.HandleChatCompletions(w, req, pool)

		if w.Code != http.StatusOK {
			t.Fatalf("cursor turn %d failed: %d %s", turn, w.Code, w.Body.String())
		}
		bodyStr := w.Body.String()

		switch turn {
		case 1:
			if !strings.Contains(bodyStr, "Cursor Planner") || !strings.Contains(bodyStr, "Cursor Auditor") {
				t.Fatalf("turn 1 expected both subagents: %s", bodyStr)
			}
			messages = append(messages,
				map[string]any{
					"role": "assistant",
					"tool_calls": []map[string]any{
						{"id": "c1", "type": "function", "function": map[string]any{"name": "invoke_subagent", "arguments": `{"role":"Cursor Planner","prompt":"Plan refactor"}`}},
						{"id": "c2", "type": "function", "function": map[string]any{"name": "invoke_subagent", "arguments": `{"role":"Cursor Auditor","prompt":"Audit security"}`}},
					},
				},
				map[string]any{"role": "tool", "tool_call_id": "c1", "content": "Plan approved."},
				map[string]any{"role": "tool", "tool_call_id": "c2", "content": "Security approved."},
			)
		case 2:
			if !strings.Contains(bodyStr, "read_file") || !strings.Contains(bodyStr, "grep_search") || !strings.Contains(bodyStr, "run_terminal_command") {
				t.Fatalf("turn 2 expected all 3 parallel tools: %s", bodyStr)
			}
			messages = append(messages,
				map[string]any{
					"role": "assistant",
					"tool_calls": []map[string]any{
						{"id": "c3", "type": "function", "function": map[string]any{"name": "read_file", "arguments": `{"path":"main.go"}`}},
						{"id": "c4", "type": "function", "function": map[string]any{"name": "grep_search", "arguments": `{"query":"HandleChatCompletions"}`}},
						{"id": "c5", "type": "function", "function": map[string]any{"name": "run_terminal_command", "arguments": `{"command":"go test ./pkg/proxy"}`}},
					},
				},
				map[string]any{"role": "tool", "tool_call_id": "c3", "content": "package main"},
				map[string]any{"role": "tool", "tool_call_id": "c4", "content": "pkg/bridge/openai.go:21"},
				map[string]any{"role": "tool", "tool_call_id": "c5", "content": "ok pkg/proxy 0.2s"},
			)
		case 3:
			if !strings.Contains(bodyStr, "Cursor Verifier") || !strings.Contains(bodyStr, "edit_file") {
				t.Fatalf("turn 3 expected hybrid subagent + tool: %s", bodyStr)
			}
			messages = append(messages,
				map[string]any{
					"role": "assistant",
					"tool_calls": []map[string]any{
						{"id": "c6", "type": "function", "function": map[string]any{"name": "invoke_subagent", "arguments": `{"role":"Cursor Verifier","prompt":"Verify test suite"}`}},
						{"id": "c7", "type": "function", "function": map[string]any{"name": "edit_file", "arguments": `{"path":"patch.go","code":"package main"}`}},
					},
				},
				map[string]any{"role": "tool", "tool_call_id": "c6", "content": "Tests verified."},
				map[string]any{"role": "tool", "tool_call_id": "c7", "content": "Patch written."},
			)
		case 4:
			if !strings.Contains(bodyStr, "go version") {
				t.Fatalf("turn 4 expected go version: %s", bodyStr)
			}
			messages = append(messages,
				map[string]any{
					"role": "assistant",
					"tool_calls": []map[string]any{
						{"id": "c8", "type": "function", "function": map[string]any{"name": "run_terminal_command", "arguments": `{"command":"go version"}`}},
					},
				},
				map[string]any{"role": "tool", "tool_call_id": "c8", "content": "go1.26 darwin/arm64"},
			)
		case 5:
			if !strings.Contains(bodyStr, "Cursor ultimate multi-subagent") {
				t.Fatalf("turn 5 expected conclusion: %s", bodyStr)
			}
		}
	}
}

// Same Ultimate test on ANTIGRAVITY Gemini wire format (:streamGenerateContent)
func TestGeminiWeb_Ultimate_Antigravity(t *testing.T) {
	backend := &mockGeminiWebBackend{
		onTurn: func(turn int, req *types.ChatRequest) string {
			switch turn {
			case 1:
				// Parallel multi subagents
				return `<tool_call>
{"name":"invoke_subagent","arguments":{"Role":"AGY Master Planner","Prompt":"Create system plan"}}
</tool_call>
<tool_call>
{"name":"invoke_subagent","arguments":{"Role":"AGY Safety Auditor","Prompt":"Perform safety checks"}}
</tool_call>`
			case 2:
				// Parallel multi tools
				return `<tool_call>
{"name":"view_file","arguments":{"AbsolutePath":"main.go"}}
</tool_call>
<tool_call>
{"name":"run_command","arguments":{"CommandLine":"pwd"}}
</tool_call>
<tool_call>
{"name":"list_dir","arguments":{"DirectoryPath":"pkg"}}
</tool_call>`
			case 3:
				// Hybrid subagent + tool
				return `<tool_call>
{"name":"invoke_subagent","arguments":{"Role":"AGY Patch Engineer","Prompt":"Produce refactor"}}
</tool_call>
<tool_call>
{"name":"view_file","arguments":{"AbsolutePath":"pkg/bridge/gemini.go"}}
</tool_call>`
			case 4:
				return `<tool_call>
{"name":"run_command","arguments":{"CommandLine":"go test ./pkg/bridge"}}
</tool_call>`
			default:
				return "Antigravity AGY ultimate multi-subagent multi-tool multi-loop passed on gemini:web:01."
			}
		},
	}
	pool := router.NewAccountPoolRouter([]types.ProviderAdapter{backend})

	agyTools := []map[string]any{
		{
			"functionDeclarations": []map[string]any{
				{"name": "run_command", "parameters": map[string]any{"type": "object", "properties": map[string]any{"CommandLine": map[string]any{"type": "string"}}}},
				{"name": "view_file", "parameters": map[string]any{"type": "object", "properties": map[string]any{"AbsolutePath": map[string]any{"type": "string"}}}},
				{"name": "list_dir", "parameters": map[string]any{"type": "object", "properties": map[string]any{"DirectoryPath": map[string]any{"type": "string"}}}},
				{"name": "invoke_subagent", "parameters": map[string]any{"type": "object", "properties": map[string]any{"Role": map[string]any{"type": "string"}, "Prompt": map[string]any{"type": "string"}}}},
			},
		},
	}

	var contents []map[string]any
	contents = append(contents, map[string]any{
		"role":  "user",
		"parts": []map[string]any{{"text": "Execute ultimate AGY multi-subagent workflow."}},
	})

	for turn := 1; turn <= 5; turn++ {
		reqBody := map[string]any{
			"contents": contents,
			"tools":    agyTools,
		}
		b, _ := json.Marshal(reqBody)
		req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-pro:streamGenerateContent", bytes.NewReader(b))
		req.Header.Set("X-Provider", "gemini:web:01")
		w := httptest.NewRecorder()
		bridge.HandleGeminiGenerateContent(w, req, pool)

		if w.Code != http.StatusOK {
			t.Fatalf("agy turn %d failed: %d %s", turn, w.Code, w.Body.String())
		}
		bodyStr := w.Body.String()

		switch turn {
		case 1:
			if !strings.Contains(bodyStr, "AGY Master Planner") || !strings.Contains(bodyStr, "AGY Safety Auditor") {
				t.Fatalf("turn 1 expected both parallel subagents in parts: %s", bodyStr)
			}
			contents = append(contents,
				map[string]any{
					"role": "model",
					"parts": []map[string]any{
						{"functionCall": map[string]any{"name": "invoke_subagent", "args": map[string]any{"Role": "AGY Master Planner", "Prompt": "Create system plan"}}},
						{"functionCall": map[string]any{"name": "invoke_subagent", "args": map[string]any{"Role": "AGY Safety Auditor", "Prompt": "Perform safety checks"}}},
					},
				},
				map[string]any{
					"role": "user",
					"parts": []map[string]any{
						{"functionResponse": map[string]any{"name": "invoke_subagent", "response": map[string]any{"result": "Plan approved."}}},
						{"functionResponse": map[string]any{"name": "invoke_subagent", "response": map[string]any{"result": "Safety approved."}}},
					},
				},
			)
		case 2:
			if !strings.Contains(bodyStr, "view_file") || !strings.Contains(bodyStr, "run_command") || !strings.Contains(bodyStr, "list_dir") {
				t.Fatalf("turn 2 expected all 3 parallel tools in parts: %s", bodyStr)
			}
			contents = append(contents,
				map[string]any{
					"role": "model",
					"parts": []map[string]any{
						{"functionCall": map[string]any{"name": "view_file", "args": map[string]any{"AbsolutePath": "main.go"}}},
						{"functionCall": map[string]any{"name": "run_command", "args": map[string]any{"CommandLine": "pwd"}}},
						{"functionCall": map[string]any{"name": "list_dir", "args": map[string]any{"DirectoryPath": "pkg"}}},
					},
				},
				map[string]any{
					"role": "user",
					"parts": []map[string]any{
						{"functionResponse": map[string]any{"name": "view_file", "response": map[string]any{"content": "package main"}}},
						{"functionResponse": map[string]any{"name": "run_command", "response": map[string]any{"output": "/amux"}}},
						{"functionResponse": map[string]any{"name": "list_dir", "response": map[string]any{"children": []string{"bridge", "cli"}}}},
					},
				},
			)
		case 3:
			if !strings.Contains(bodyStr, "AGY Patch Engineer") || !strings.Contains(bodyStr, "pkg/bridge/gemini.go") {
				t.Fatalf("turn 3 expected hybrid subagent + tool in parts: %s", bodyStr)
			}
			contents = append(contents,
				map[string]any{
					"role": "model",
					"parts": []map[string]any{
						{"functionCall": map[string]any{"name": "invoke_subagent", "args": map[string]any{"Role": "AGY Patch Engineer", "Prompt": "Produce refactor"}}},
						{"functionCall": map[string]any{"name": "view_file", "args": map[string]any{"AbsolutePath": "pkg/bridge/gemini.go"}}},
					},
				},
				map[string]any{
					"role": "user",
					"parts": []map[string]any{
						{"functionResponse": map[string]any{"name": "invoke_subagent", "response": map[string]any{"result": "Patch ready."}}},
						{"functionResponse": map[string]any{"name": "view_file", "response": map[string]any{"content": "package bridge"}}},
					},
				},
			)
		case 4:
			if !strings.Contains(bodyStr, "go test ./pkg/bridge") {
				t.Fatalf("turn 4 expected run_command: %s", bodyStr)
			}
			contents = append(contents,
				map[string]any{
					"role": "model",
					"parts": []map[string]any{
						{"functionCall": map[string]any{"name": "run_command", "args": map[string]any{"CommandLine": "go test ./pkg/bridge"}}},
					},
				},
				map[string]any{
					"role": "user",
					"parts": []map[string]any{
						{"functionResponse": map[string]any{"name": "run_command", "response": map[string]any{"output": "PASS"}}},
					},
				},
			)
		case 5:
			if !strings.Contains(bodyStr, "Antigravity AGY ultimate multi-subagent") {
				t.Fatalf("turn 5 expected final conclusion: %s", bodyStr)
			}
		}
	}
}

// Same Ultimate test on CODEX wire format
func TestGeminiWeb_Ultimate_Codex(t *testing.T) {
	backend := &mockGeminiWebBackend{
		onTurn: func(turn int, req *types.ChatRequest) string {
			switch turn {
			case 1:
				// Parallel multi subagents
				return `<tool_call>
{"name":"invoke_subagent","arguments":{"role":"Codex Strategy Agent","prompt":"Formulate optimization plan"}}
</tool_call>
<tool_call>
{"name":"invoke_subagent","arguments":{"role":"Codex Quality Gate","prompt":"Validate test standards"}}
</tool_call>`
			case 2:
				// Parallel multi tools
				return `<tool_call>
{"name":"read_file","arguments":{"file_path":"go.mod"}}
</tool_call>
<tool_call>
{"name":"grep_search","arguments":{"pattern":"gemini_web"}}
</tool_call>
<tool_call>
{"name":"exec_command","arguments":{"cmd":"git status"}}
</tool_call>`
			case 3:
				// Hybrid subagent + tool
				return `<tool_call>
{"name":"invoke_subagent","arguments":{"role":"Codex Implementer","prompt":"Apply code optimization"}}
</tool_call>
<tool_call>
{"name":"apply_diff","arguments":{"file_path":"pkg/tools/codex.go","diff":"@@ -1 +1 @@"}}
</tool_call>`
			case 4:
				return `<tool_call>
{"name":"exec_command","arguments":{"cmd":"go test ./pkg/tools"}}
</tool_call>`
			default:
				return "Codex ultimate multi-subagent multi-tool multi-loop passed on gemini:web:01."
			}
		},
	}
	pool := router.NewAccountPoolRouter([]types.ProviderAdapter{backend})

	var messages []map[string]any
	messages = append(messages, map[string]any{"role": "user", "content": "Execute ultimate Codex multi-subagent workflow."})

	for turn := 1; turn <= 5; turn++ {
		reqBody := map[string]any{
			"model":    "gpt-4o",
			"stream":   true,
			"tools":    codexToolsCatalog(),
			"messages": messages,
		}
		b, _ := json.Marshal(reqBody)
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(b))
		req.Header.Set("X-Provider", "gemini:web:01")
		w := httptest.NewRecorder()
		bridge.HandleChatCompletions(w, req, pool)

		if w.Code != http.StatusOK {
			t.Fatalf("codex turn %d failed: %d %s", turn, w.Code, w.Body.String())
		}
		bodyStr := w.Body.String()

		switch turn {
		case 1:
			if !strings.Contains(bodyStr, "Codex Strategy Agent") || !strings.Contains(bodyStr, "Codex Quality Gate") {
				t.Fatalf("turn 1 expected both subagents: %s", bodyStr)
			}
			messages = append(messages,
				map[string]any{
					"role": "assistant",
					"tool_calls": []map[string]any{
						{"id": "cdx_1", "type": "function", "function": map[string]any{"name": "invoke_subagent", "arguments": `{"role":"Codex Strategy Agent","prompt":"Formulate optimization plan"}`}},
						{"id": "cdx_2", "type": "function", "function": map[string]any{"name": "invoke_subagent", "arguments": `{"role":"Codex Quality Gate","prompt":"Validate test standards"}`}},
					},
				},
				map[string]any{"role": "tool", "tool_call_id": "cdx_1", "content": "Strategy plan approved."},
				map[string]any{"role": "tool", "tool_call_id": "cdx_2", "content": "Standards validated."},
			)
		case 2:
			if !strings.Contains(bodyStr, "read_file") || !strings.Contains(bodyStr, "grep_search") || !strings.Contains(bodyStr, "exec_command") {
				t.Fatalf("turn 2 expected all 3 parallel tools: %s", bodyStr)
			}
			messages = append(messages,
				map[string]any{
					"role": "assistant",
					"tool_calls": []map[string]any{
						{"id": "cdx_3", "type": "function", "function": map[string]any{"name": "read_file", "arguments": `{"file_path":"go.mod"}`}},
						{"id": "cdx_4", "type": "function", "function": map[string]any{"name": "grep_search", "arguments": `{"pattern":"gemini_web"}`}},
						{"id": "cdx_5", "type": "function", "function": map[string]any{"name": "exec_command", "arguments": `{"cmd":"git status"}`}},
					},
				},
				map[string]any{"role": "tool", "tool_call_id": "cdx_3", "content": "module amux-accounts"},
				map[string]any{"role": "tool", "tool_call_id": "cdx_4", "content": "gemini_web.go:28"},
				map[string]any{"role": "tool", "tool_call_id": "cdx_5", "content": "clean"},
			)
		case 3:
			if !strings.Contains(bodyStr, "Codex Implementer") || !strings.Contains(bodyStr, "apply_diff") {
				t.Fatalf("turn 3 expected hybrid subagent + tool: %s", bodyStr)
			}
			messages = append(messages,
				map[string]any{
					"role": "assistant",
					"tool_calls": []map[string]any{
						{"id": "cdx_6", "type": "function", "function": map[string]any{"name": "invoke_subagent", "arguments": `{"role":"Codex Implementer","prompt":"Apply code optimization"}`}},
						{"id": "cdx_7", "type": "function", "function": map[string]any{"name": "apply_diff", "arguments": `{"file_path":"pkg/tools/codex.go","diff":"@@ -1 +1 @@"}`}},
					},
				},
				map[string]any{"role": "tool", "tool_call_id": "cdx_6", "content": "Optimization applied."},
				map[string]any{"role": "tool", "tool_call_id": "cdx_7", "content": "Patch applied cleanly."},
			)
		case 4:
			if !strings.Contains(bodyStr, "exec_command") {
				t.Fatalf("turn 4 expected exec_command: %s", bodyStr)
			}
			messages = append(messages,
				map[string]any{
					"role": "assistant",
					"tool_calls": []map[string]any{
						{"id": "cdx_8", "type": "function", "function": map[string]any{"name": "exec_command", "arguments": `{"cmd":"go test ./pkg/tools"}`}},
					},
				},
				map[string]any{"role": "tool", "tool_call_id": "cdx_8", "content": "ok pkg/tools 0.15s"},
			)
		case 5:
			if !strings.Contains(bodyStr, "Codex ultimate multi-subagent") {
				t.Fatalf("turn 5 expected conclusion: %s", bodyStr)
			}
		}
	}
}



