package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"amux-accounts/pkg/router"
	"amux-accounts/pkg/tools"
	"amux-accounts/pkg/types"
)

// mockClaudeWebBackend simulates ClaudeWebAdapter ("claude:web:01")
// operating through the proxy with the webloop protocol (<tool_call>).
type mockClaudeWebBackend struct {
	mu           sync.Mutex
	turnCount    int
	onTurn       func(turn int, req *types.ChatRequest) string
	recordedReqs []*types.ChatRequest
}

func (m *mockClaudeWebBackend) ID() string    { return "claude:web:01" }
func (m *mockClaudeWebBackend) Priority() int { return 1 }
func (m *mockClaudeWebBackend) SendMessageStream(ctx context.Context, req *types.ChatRequest) (<-chan types.StreamChunk, error) {
	m.mu.Lock()
	m.turnCount++
	turn := m.turnCount
	m.recordedReqs = append(m.recordedReqs, req)
	text := m.onTurn(turn, req)
	m.mu.Unlock()

	rawChan := make(chan types.StreamChunk, 2)
	rawChan <- types.StreamChunk{ID: "claude:web:01", Content: text}
	rawChan <- types.StreamChunk{ID: "claude:web:01", Done: true}
	close(rawChan)

	// ClaudeWebAdapter wraps with MaybeWrapWebStream:
	return tools.MaybeWrapWebStream("claude:web:01", req, rawChan), nil
}

// setupClaudeWebProxyServer builds a test proxy HTTP server with claude:web:01
// in the pool and zero subscriptions active.
func setupClaudeWebProxyServer(t *testing.T, backend *mockClaudeWebBackend) *httptest.Server {
	t.Helper()
	t.Setenv("AM_HOME", t.TempDir())
	t.Setenv("ANTHROPIC_API_KEY", "")

	pool := router.NewAccountPoolRouter([]types.ProviderAdapter{backend})
	pool.SetDirectory([]types.ProviderAdapter{backend})

	rot := NewRotator("claude")
	life := NewLifecycle()
	mode := &ProxyMode{}
	mode.Set("provider") // Enforce provider mode, zero subscriptions

	rp := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("FATAL: reverse-proxy to subscription was called! Expected only claude:web:01 pool route.")
	})

	sw := &swappableHandler{}
	h := newHandler(rot, life, mode, pool, pool, rp, "https://api.anthropic.com", sw, "", func() {})
	sw.Set(h)

	return httptest.NewServer(sw)
}

// ============================================================================
// 1. CLAUDE CODE THROUGH PROXY (POST /v1/messages) on claude:web:01
// ============================================================================
func TestProxy_ClaudeWeb_MultiTools_MultiAgent_MultiSubAgent_MultiLoop_Claude(t *testing.T) {
	backend := &mockClaudeWebBackend{
		onTurn: func(turn int, req *types.ChatRequest) string {
			switch turn {
			case 1:
				// PARALLEL MULTI-SUBAGENTS: 2 subagents dispatched at once
				return `<tool_call>
{"name":"invoke_subagent","arguments":{"role":"Architecture Auditor","prompt":"Audit proxy gateway"}}
</tool_call>
<tool_call>
{"name":"invoke_subagent","arguments":{"role":"Security Auditor","prompt":"Check tunnel security"}}
</tool_call>`
			case 2:
				// PARALLEL MULTI-TOOLS: 3 regular tools executed simultaneously
				return `<tool_call>
{"name":"Read","arguments":{"file_path":"pkg/proxy/server.go"}}
</tool_call>
<tool_call>
{"name":"Read","arguments":{"file_path":"pkg/proxy/tunnel.go"}}
</tool_call>
<tool_call>
{"name":"Bash","arguments":{"command":"go test ./pkg/proxy"}}
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
				// Verification loop
				return `<tool_call>
{"name":"Bash","arguments":{"command":"go test ./pkg/tools"}}
</tool_call>`
			default:
				// Final synthesis turn
				return "Claude Code through proxy completed 5-turn multi-agent, multi-subagent, multi-tool loop on claude:web:01."
			}
		},
	}

	server := setupClaudeWebProxyServer(t, backend)
	defer server.Close()

	client := server.Client()

	claudeTools := []map[string]any{
		{"name": "Bash", "input_schema": map[string]any{"type": "object", "properties": map[string]any{"command": map[string]any{"type": "string"}}}},
		{"name": "Read", "input_schema": map[string]any{"type": "object", "properties": map[string]any{"file_path": map[string]any{"type": "string"}}}},
		{"name": "invoke_subagent", "input_schema": map[string]any{"type": "object", "properties": map[string]any{"role": map[string]any{"type": "string"}, "prompt": map[string]any{"type": "string"}}}},
	}

	var messages []map[string]any
	messages = append(messages, map[string]any{"role": "user", "content": "Start multi-agent multi-subagent audit via proxy."})

	for turn := 1; turn <= 5; turn++ {
		reqBody := map[string]any{
			"model":    "claude-sonnet-5",
			"stream":   true,
			"tools":    claudeTools,
			"messages": messages,
		}
		b, _ := json.Marshal(reqBody)

		req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/messages", bytes.NewReader(b))
		if err != nil {
			t.Fatalf("turn %d create request: %v", turn, err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Provider", "claude:web:01")

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("turn %d http error: %v", turn, err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("turn %d status=%d", turn, resp.StatusCode)
		}

		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		_ = resp.Body.Close()
		bodyStr := buf.String()

		switch turn {
		case 1:
			// Verify BOTH subagents arrived through the proxy in the same turn
			if !strings.Contains(bodyStr, "Architecture Auditor") || !strings.Contains(bodyStr, "Security Auditor") {
				t.Fatalf("turn 1 expected both subagents through proxy: %s", bodyStr)
			}
			messages = append(messages,
				map[string]any{
					"role": "assistant",
					"content": []map[string]any{
						{"type": "tool_use", "id": "call_sub_1", "name": "invoke_subagent", "input": map[string]any{"role": "Architecture Auditor", "prompt": "Audit proxy gateway"}},
						{"type": "tool_use", "id": "call_sub_2", "name": "invoke_subagent", "input": map[string]any{"role": "Security Auditor", "prompt": "Check tunnel security"}},
					},
				},
				map[string]any{
					"role": "user",
					"content": []map[string]any{
						{"type": "tool_result", "tool_use_id": "call_sub_1", "content": `{"status":"arch_ok"}`},
						{"type": "tool_result", "tool_use_id": "call_sub_2", "content": `{"status":"sec_ok"}`},
					},
				},
			)
		case 2:
			// Verify ALL 3 parallel tools arrived through the proxy in the same turn
			if !strings.Contains(bodyStr, "pkg/proxy/server.go") || !strings.Contains(bodyStr, "pkg/proxy/tunnel.go") || !strings.Contains(bodyStr, "go test ./pkg/proxy") {
				t.Fatalf("turn 2 expected all 3 parallel tools through proxy: %s", bodyStr)
			}
			messages = append(messages,
				map[string]any{
					"role": "assistant",
					"content": []map[string]any{
						{"type": "tool_use", "id": "call_t_1", "name": "Read", "input": map[string]any{"file_path": "pkg/proxy/server.go"}},
						{"type": "tool_use", "id": "call_t_2", "name": "Read", "input": map[string]any{"file_path": "pkg/proxy/tunnel.go"}},
						{"type": "tool_use", "id": "call_t_3", "name": "Bash", "input": map[string]any{"command": "go test ./pkg/proxy"}},
					},
				},
				map[string]any{
					"role": "user",
					"content": []map[string]any{
						{"type": "tool_result", "tool_use_id": "call_t_1", "content": "package proxy"},
						{"type": "tool_result", "tool_use_id": "call_t_2", "content": "package proxy // tunnel"},
						{"type": "tool_result", "tool_use_id": "call_t_3", "content": "ok pkg/proxy 0.15s"},
					},
				},
			)
		case 3:
			// Verify HYBRID subagent + tool arrived through the proxy in the same turn
			if !strings.Contains(bodyStr, "Refactor Specialist") || !strings.Contains(bodyStr, "git diff") {
				t.Fatalf("turn 3 expected hybrid subagent + tool through proxy: %s", bodyStr)
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
						{"type": "tool_result", "tool_use_id": "call_h_1", "content": "Refactored cleanly."},
						{"type": "tool_result", "tool_use_id": "call_h_2", "content": "Working tree clean."},
					},
				},
			)
		case 4:
			if !strings.Contains(bodyStr, "go test ./pkg/tools") {
				t.Fatalf("turn 4 expected verification tool through proxy: %s", bodyStr)
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
						{"type": "tool_result", "tool_use_id": "call_v_1", "content": "ok pkg/tools 0.25s"},
					},
				},
			)
		case 5:
			if !strings.Contains(bodyStr, "Claude Code through proxy completed") {
				t.Fatalf("turn 5 expected completion message: %s", bodyStr)
			}
		}
	}
}

// ============================================================================
// 2. CURSOR THROUGH PROXY (POST /v1/chat/completions) on claude:web:01
// ============================================================================
func TestProxy_ClaudeWeb_MultiTools_MultiAgent_MultiSubAgent_MultiLoop_Cursor(t *testing.T) {
	backend := &mockClaudeWebBackend{
		onTurn: func(turn int, req *types.ChatRequest) string {
			switch turn {
			case 1:
				return `<tool_call>
{"name":"invoke_subagent","arguments":{"role":"Cursor Planner","prompt":"Plan gateway refactor"}}
</tool_call>
<tool_call>
{"name":"invoke_subagent","arguments":{"role":"Cursor Auditor","prompt":"Audit proxy security"}}
</tool_call>`
			case 2:
				return `<tool_call>
{"name":"read_file","arguments":{"path":"pkg/proxy/server.go"}}
</tool_call>
<tool_call>
{"name":"grep_search","arguments":{"query":"HandleChatCompletions"}}
</tool_call>
<tool_call>
{"name":"run_terminal_command","arguments":{"command":"go test ./pkg/proxy"}}
</tool_call>`
			case 3:
				return `<tool_call>
{"name":"invoke_subagent","arguments":{"role":"Cursor Patch Agent","prompt":"Apply patch"}}
</tool_call>
<tool_call>
{"name":"edit_file","arguments":{"path":"patch.go","code":"package main"}}
</tool_call>`
			case 4:
				return `<tool_call>
{"name":"run_terminal_command","arguments":{"command":"go version"}}
</tool_call>`
			default:
				return "Cursor through proxy completed 5-turn multi-agent, multi-subagent, multi-tool loop on claude:web:01."
			}
		},
	}

	server := setupClaudeWebProxyServer(t, backend)
	defer server.Close()

	client := server.Client()

	cursorTools := []map[string]any{
		{"type": "function", "function": map[string]any{"name": "run_terminal_command", "parameters": map[string]any{"type": "object", "properties": map[string]any{"command": map[string]any{"type": "string"}}}}},
		{"type": "function", "function": map[string]any{"name": "read_file", "parameters": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}}}},
		{"type": "function", "function": map[string]any{"name": "edit_file", "parameters": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}, "code": map[string]any{"type": "string"}}}}},
		{"type": "function", "function": map[string]any{"name": "grep_search", "parameters": map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}}}},
		{"type": "function", "function": map[string]any{"name": "invoke_subagent", "parameters": map[string]any{"type": "object", "properties": map[string]any{"role": map[string]any{"type": "string"}, "prompt": map[string]any{"type": "string"}}}}},
	}

	var messages []map[string]any
	messages = append(messages, map[string]any{"role": "user", "content": "Execute Cursor workflow through proxy."})

	for turn := 1; turn <= 5; turn++ {
		reqBody := map[string]any{
			"model":    "gpt-4o",
			"stream":   true,
			"tools":    cursorTools,
			"messages": messages,
		}
		b, _ := json.Marshal(reqBody)

		req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", bytes.NewReader(b))
		if err != nil {
			t.Fatalf("turn %d create request: %v", turn, err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Provider", "claude:web:01")

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("turn %d http error: %v", turn, err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("turn %d status=%d", turn, resp.StatusCode)
		}

		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		_ = resp.Body.Close()
		bodyStr := buf.String()

		switch turn {
		case 1:
			if !strings.Contains(bodyStr, "Cursor Planner") || !strings.Contains(bodyStr, "Cursor Auditor") {
				t.Fatalf("turn 1 expected both subagents: %s", bodyStr)
			}
			messages = append(messages,
				map[string]any{
					"role": "assistant",
					"tool_calls": []map[string]any{
						{"id": "c1", "type": "function", "function": map[string]any{"name": "invoke_subagent", "arguments": `{"role":"Cursor Planner","prompt":"Plan gateway refactor"}`}},
						{"id": "c2", "type": "function", "function": map[string]any{"name": "invoke_subagent", "arguments": `{"role":"Cursor Auditor","prompt":"Audit proxy security"}`}},
					},
				},
				map[string]any{"role": "tool", "tool_call_id": "c1", "content": "Plan approved."},
				map[string]any{"role": "tool", "tool_call_id": "c2", "content": "Audit passed."},
			)
		case 2:
			if !strings.Contains(bodyStr, "read_file") || !strings.Contains(bodyStr, "grep_search") || !strings.Contains(bodyStr, "run_terminal_command") {
				t.Fatalf("turn 2 expected 3 parallel tools: %s", bodyStr)
			}
			messages = append(messages,
				map[string]any{
					"role": "assistant",
					"tool_calls": []map[string]any{
						{"id": "c3", "type": "function", "function": map[string]any{"name": "read_file", "arguments": `{"path":"pkg/proxy/server.go"}`}},
						{"id": "c4", "type": "function", "function": map[string]any{"name": "grep_search", "arguments": `{"query":"HandleChatCompletions"}`}},
						{"id": "c5", "type": "function", "function": map[string]any{"name": "run_terminal_command", "arguments": `{"command":"go test ./pkg/proxy"}`}},
					},
				},
				map[string]any{"role": "tool", "tool_call_id": "c3", "content": "package proxy"},
				map[string]any{"role": "tool", "tool_call_id": "c4", "content": "HandleChatCompletions"},
				map[string]any{"role": "tool", "tool_call_id": "c5", "content": "PASS"},
			)
		case 3:
			if !strings.Contains(bodyStr, "Cursor Patch Agent") || !strings.Contains(bodyStr, "edit_file") {
				t.Fatalf("turn 3 expected hybrid subagent + tool: %s", bodyStr)
			}
			messages = append(messages,
				map[string]any{
					"role": "assistant",
					"tool_calls": []map[string]any{
						{"id": "c6", "type": "function", "function": map[string]any{"name": "invoke_subagent", "arguments": `{"role":"Cursor Patch Agent","prompt":"Apply patch"}`}},
						{"id": "c7", "type": "function", "function": map[string]any{"name": "edit_file", "arguments": `{"path":"patch.go","code":"package main"}`}},
					},
				},
				map[string]any{"role": "tool", "tool_call_id": "c6", "content": "Patch ready."},
				map[string]any{"role": "tool", "tool_call_id": "c7", "content": "Edit applied."},
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
				map[string]any{"role": "tool", "tool_call_id": "c8", "content": "go version go1.26 darwin/arm64"},
			)
		case 5:
			if !strings.Contains(bodyStr, "Cursor through proxy completed") {
				t.Fatalf("turn 5 expected completion message: %s", bodyStr)
			}
		}
	}
}

// ============================================================================
// 3. ANTIGRAVITY THROUGH PROXY (POST /v1beta/models/...:streamGenerateContent)
// ============================================================================
func TestProxy_ClaudeWeb_MultiTools_MultiAgent_MultiSubAgent_MultiLoop_Antigravity(t *testing.T) {
	backend := &mockClaudeWebBackend{
		onTurn: func(turn int, req *types.ChatRequest) string {
			switch turn {
			case 1:
				return `<tool_call>
{"name":"invoke_subagent","arguments":{"Role":"AGY System Architect","Prompt":"Inspect proxy model routing"}}
</tool_call>
<tool_call>
{"name":"invoke_subagent","arguments":{"Role":"AGY Verifier","Prompt":"Check Gemini SSE wire output"}}
</tool_call>`
			case 2:
				return `<tool_call>
{"name":"view_file","arguments":{"AbsolutePath":"main.go"}}
</tool_call>
<tool_call>
{"name":"run_command","arguments":{"CommandLine":"pwd"}}
</tool_call>
<tool_call>
{"name":"list_dir","arguments":{"DirectoryPath":"pkg/proxy"}}
</tool_call>`
			case 3:
				return `<tool_call>
{"name":"invoke_subagent","arguments":{"Role":"AGY Patch Engineer","Prompt":"Optimize proxy stream flush"}}
</tool_call>
<tool_call>
{"name":"view_file","arguments":{"AbsolutePath":"pkg/bridge/gemini.go"}}
</tool_call>`
			case 4:
				return `<tool_call>
{"name":"run_command","arguments":{"CommandLine":"go test ./pkg/bridge"}}
</tool_call>`
			default:
				return "Antigravity AGY through proxy completed 5-turn multi-agent, multi-subagent, multi-tool loop on claude:web:01."
			}
		},
	}

	server := setupClaudeWebProxyServer(t, backend)
	defer server.Close()

	client := server.Client()

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
		"parts": []map[string]any{{"text": "Execute AGY multi-agent workflow through proxy on claude:web:01."}},
	})

	for turn := 1; turn <= 5; turn++ {
		reqBody := map[string]any{
			"contents": contents,
			"tools":    agyTools,
		}
		b, _ := json.Marshal(reqBody)

		req, err := http.NewRequest(http.MethodPost, server.URL+"/v1beta/models/gemini-2.5-pro:streamGenerateContent", bytes.NewReader(b))
		if err != nil {
			t.Fatalf("turn %d create request: %v", turn, err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Provider", "claude:web:01")

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("turn %d http error: %v", turn, err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("turn %d status=%d", turn, resp.StatusCode)
		}

		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		_ = resp.Body.Close()
		bodyStr := buf.String()

		switch turn {
		case 1:
			if !strings.Contains(bodyStr, "AGY System Architect") || !strings.Contains(bodyStr, "AGY Verifier") {
				t.Fatalf("turn 1 expected both parallel subagents in parts: %s", bodyStr)
			}
			contents = append(contents,
				map[string]any{
					"role": "model",
					"parts": []map[string]any{
						{"functionCall": map[string]any{"name": "invoke_subagent", "args": map[string]any{"Role": "AGY System Architect", "Prompt": "Inspect proxy model routing"}}},
						{"functionCall": map[string]any{"name": "invoke_subagent", "args": map[string]any{"Role": "AGY Verifier", "Prompt": "Check Gemini SSE wire output"}}},
					},
				},
				map[string]any{
					"role": "user",
					"parts": []map[string]any{
						{"functionResponse": map[string]any{"name": "invoke_subagent", "response": map[string]any{"result": "Architecture approved."}}},
						{"functionResponse": map[string]any{"name": "invoke_subagent", "response": map[string]any{"result": "Output verified."}}},
					},
				},
			)
		case 2:
			if !strings.Contains(bodyStr, "view_file") || !strings.Contains(bodyStr, "run_command") || !strings.Contains(bodyStr, "list_dir") {
				t.Fatalf("turn 2 expected 3 parallel tools in parts: %s", bodyStr)
			}
			contents = append(contents,
				map[string]any{
					"role": "model",
					"parts": []map[string]any{
						{"functionCall": map[string]any{"name": "view_file", "args": map[string]any{"AbsolutePath": "main.go"}}},
						{"functionCall": map[string]any{"name": "run_command", "args": map[string]any{"CommandLine": "pwd"}}},
						{"functionCall": map[string]any{"name": "list_dir", "args": map[string]any{"DirectoryPath": "pkg/proxy"}}},
					},
				},
				map[string]any{
					"role": "user",
					"parts": []map[string]any{
						{"functionResponse": map[string]any{"name": "view_file", "response": map[string]any{"content": "package main"}}},
						{"functionResponse": map[string]any{"name": "run_command", "response": map[string]any{"output": "/workspace"}}},
						{"functionResponse": map[string]any{"name": "list_dir", "response": map[string]any{"children": []string{"server.go", "tunnel.go"}}}},
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
						{"functionCall": map[string]any{"name": "invoke_subagent", "args": map[string]any{"Role": "AGY Patch Engineer", "Prompt": "Optimize proxy stream flush"}}},
						{"functionCall": map[string]any{"name": "view_file", "args": map[string]any{"AbsolutePath": "pkg/bridge/gemini.go"}}},
					},
				},
				map[string]any{
					"role": "user",
					"parts": []map[string]any{
						{"functionResponse": map[string]any{"name": "invoke_subagent", "response": map[string]any{"result": "Stream flush optimized."}}},
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
						{"functionResponse": map[string]any{"name": "run_command", "response": map[string]any{"output": "PASS ok pkg/bridge"}}},
					},
				},
			)
		case 5:
			if !strings.Contains(bodyStr, "Antigravity AGY through proxy completed") {
				t.Fatalf("turn 5 expected final output: %s", bodyStr)
			}
		}
	}
}

// ============================================================================
// 4. CODEX THROUGH PROXY (POST /v1/chat/completions) on claude:web:01
// ============================================================================
func TestProxy_ClaudeWeb_MultiTools_MultiAgent_MultiSubAgent_MultiLoop_Codex(t *testing.T) {
	backend := &mockClaudeWebBackend{
		onTurn: func(turn int, req *types.ChatRequest) string {
			switch turn {
			case 1:
				return `<tool_call>
{"name":"invoke_subagent","arguments":{"role":"Codex Strategy Agent","prompt":"Formulate test plan"}}
</tool_call>
<tool_call>
{"name":"invoke_subagent","arguments":{"role":"Codex Quality Gate","prompt":"Validate coverage"}}
</tool_call>`
			case 2:
				return `<tool_call>
{"name":"read_file","arguments":{"file_path":"go.mod"}}
</tool_call>
<tool_call>
{"name":"list_dir","arguments":{"path":"pkg/proxy"}}
</tool_call>
<tool_call>
{"name":"exec_command","arguments":{"cmd":"git status"}}
</tool_call>`
			case 3:
				return `<tool_call>
{"name":"invoke_subagent","arguments":{"role":"Codex Implementer","prompt":"Apply code patch"}}
</tool_call>
<tool_call>
{"name":"apply_diff","arguments":{"file_path":"pkg/proxy/tunnel.go","diff":"@@ -1 +1 @@"}}
</tool_call>`
			case 4:
				return `<tool_call>
{"name":"exec_command","arguments":{"cmd":"go test ./pkg/proxy"}}
</tool_call>`
			default:
				return "Codex through proxy completed 5-turn multi-agent, multi-subagent, multi-tool loop on claude:web:01."
			}
		},
	}

	server := setupClaudeWebProxyServer(t, backend)
	defer server.Close()

	client := server.Client()

	codexTools := []map[string]any{
		{"type": "function", "function": map[string]any{"name": "exec_command", "parameters": map[string]any{"type": "object", "properties": map[string]any{"cmd": map[string]any{"type": "string"}}}}},
		{"type": "function", "function": map[string]any{"name": "read_file", "parameters": map[string]any{"type": "object", "properties": map[string]any{"file_path": map[string]any{"type": "string"}}}}},
		{"type": "function", "function": map[string]any{"name": "apply_diff", "parameters": map[string]any{"type": "object", "properties": map[string]any{"file_path": map[string]any{"type": "string"}, "diff": map[string]any{"type": "string"}}}}},
		{"type": "function", "function": map[string]any{"name": "list_dir", "parameters": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}}}},
		{"type": "function", "function": map[string]any{"name": "invoke_subagent", "parameters": map[string]any{"type": "object", "properties": map[string]any{"role": map[string]any{"type": "string"}, "prompt": map[string]any{"type": "string"}}}}},
	}

	var messages []map[string]any
	messages = append(messages, map[string]any{"role": "user", "content": "Execute Codex workflow through proxy."})

	for turn := 1; turn <= 5; turn++ {
		reqBody := map[string]any{
			"model":    "gpt-4o",
			"stream":   true,
			"tools":    codexTools,
			"messages": messages,
		}
		b, _ := json.Marshal(reqBody)

		req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", bytes.NewReader(b))
		if err != nil {
			t.Fatalf("turn %d create request: %v", turn, err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Provider", "claude:web:01")

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("turn %d http error: %v", turn, err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("turn %d status=%d", turn, resp.StatusCode)
		}

		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		_ = resp.Body.Close()
		bodyStr := buf.String()

		switch turn {
		case 1:
			if !strings.Contains(bodyStr, "Codex Strategy Agent") || !strings.Contains(bodyStr, "Codex Quality Gate") {
				t.Fatalf("turn 1 expected both subagents: %s", bodyStr)
			}
			messages = append(messages,
				map[string]any{
					"role": "assistant",
					"tool_calls": []map[string]any{
						{"id": "cdx_1", "type": "function", "function": map[string]any{"name": "invoke_subagent", "arguments": `{"role":"Codex Strategy Agent","prompt":"Formulate test plan"}`}},
						{"id": "cdx_2", "type": "function", "function": map[string]any{"name": "invoke_subagent", "arguments": `{"role":"Codex Quality Gate","prompt":"Validate coverage"}`}},
					},
				},
				map[string]any{"role": "tool", "tool_call_id": "cdx_1", "content": "Strategy plan approved."},
				map[string]any{"role": "tool", "tool_call_id": "cdx_2", "content": "Quality gate passed."},
			)
		case 2:
			if !strings.Contains(bodyStr, "read_file") || !strings.Contains(bodyStr, "list_dir") || !strings.Contains(bodyStr, "exec_command") {
				t.Fatalf("turn 2 expected 3 parallel tools: %s", bodyStr)
			}
			messages = append(messages,
				map[string]any{
					"role": "assistant",
					"tool_calls": []map[string]any{
						{"id": "cdx_3", "type": "function", "function": map[string]any{"name": "read_file", "arguments": `{"file_path":"go.mod"}`}},
						{"id": "cdx_4", "type": "function", "function": map[string]any{"name": "list_dir", "arguments": `{"path":"pkg/proxy"}`}},
						{"id": "cdx_5", "type": "function", "function": map[string]any{"name": "exec_command", "arguments": `{"cmd":"git status"}`}},
					},
				},
				map[string]any{"role": "tool", "tool_call_id": "cdx_3", "content": "module amux-accounts"},
				map[string]any{"role": "tool", "tool_call_id": "cdx_4", "content": "server.go, client.go, tunnel.go"},
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
						{"id": "cdx_6", "type": "function", "function": map[string]any{"name": "invoke_subagent", "arguments": `{"role":"Codex Implementer","prompt":"Apply code patch"}`}},
						{"id": "cdx_7", "type": "function", "function": map[string]any{"name": "apply_diff", "arguments": `{"file_path":"pkg/proxy/tunnel.go","diff":"@@ -1 +1 @@"}`}},
					},
				},
				map[string]any{"role": "tool", "tool_call_id": "cdx_6", "content": "Patch ready."},
				map[string]any{"role": "tool", "tool_call_id": "cdx_7", "content": "Diff applied."},
			)
		case 4:
			if !strings.Contains(bodyStr, "exec_command") {
				t.Fatalf("turn 4 expected exec_command: %s", bodyStr)
			}
			messages = append(messages,
				map[string]any{
					"role": "assistant",
					"tool_calls": []map[string]any{
						{"id": "cdx_8", "type": "function", "function": map[string]any{"name": "exec_command", "arguments": `{"cmd":"go test ./pkg/proxy"}`}},
					},
				},
				map[string]any{"role": "tool", "tool_call_id": "cdx_8", "content": "ok pkg/proxy 0.18s"},
			)
		case 5:
			if !strings.Contains(bodyStr, "Codex through proxy completed") {
				t.Fatalf("turn 5 expected completion message: %s", bodyStr)
			}
		}
	}
}
