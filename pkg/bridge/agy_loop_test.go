package bridge_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"amux-accounts/pkg/bridge"
	"amux-accounts/pkg/router"
	"amux-accounts/pkg/types"
)

// mockAGYBackend simulates an upstream LLM (ChatGPT / Claude / Gemini API) that
// can respond with multiple tool calls, handle sequential loops, and finish.
type mockAGYBackend struct {
	mu           sync.Mutex
	turnCount    int
	onTurn       func(turn int, req *types.ChatRequest) []types.StreamChunk
	recordedReqs []*types.ChatRequest
}

func (m *mockAGYBackend) ID() string    { return "chatgpt:01" }
func (m *mockAGYBackend) Priority() int { return 1 }
func (m *mockAGYBackend) SendMessageStream(ctx context.Context, req *types.ChatRequest) (<-chan types.StreamChunk, error) {
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

// ----------------------------------------------------------------------------
// 1. Multi Loop Agent Test
// ----------------------------------------------------------------------------
// Verifies sequential multi-turn agent loop in Gemini wire format:
// Turn 1: User prompt -> Model calls `run_command`
// Turn 2: AGY returns result -> Model calls `view_file`
// Turn 3: AGY returns result -> Model calls `write_to_file`
// Turn 4: AGY returns result -> Model concludes with final text response
func TestAGY_MultiLoopAgent(t *testing.T) {
	backend := &mockAGYBackend{
		onTurn: func(turn int, req *types.ChatRequest) []types.StreamChunk {
			switch turn {
			case 1:
				return []types.StreamChunk{
					{
						ID: "chatgpt:01",
						ToolCalls: []types.ToolCall{
							{ID: "call_run_1", Name: "run_command", Arguments: `{"CommandLine":"git status"}`},
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
							{ID: "call_view_1", Name: "view_file", Arguments: `{"AbsolutePath":"/workspace/main.go"}`},
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
							{ID: "call_write_1", Name: "write_to_file", Arguments: `{"TargetFile":"/workspace/patch.go","CodeContent":"package main"}`},
						},
						FinishReason: "tool_calls",
						Done:         true,
					},
				}
			default:
				return []types.StreamChunk{
					{ID: "chatgpt:01", Content: "All changes successfully written and verified."},
					{ID: "chatgpt:01", Done: true, FinishReason: "STOP"},
				}
			}
		},
	}
	pool := router.NewAccountPoolRouter([]types.ProviderAdapter{backend})

	// Helper to send Gemini POST request to proxy
	callProxy := func(contentsJSON string) (statusCode int, body string) {
		reqPayload := fmt.Sprintf(`{
			"contents": %s,
			"tools": [{
				"functionDeclarations": [
					{"name": "run_command", "parameters": {"type": "object"}},
					{"name": "view_file", "parameters": {"type": "object"}},
					{"name": "write_to_file", "parameters": {"type": "object"}}
				]
			}]
		}`, contentsJSON)

		req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-flash:generateContent", bytes.NewBufferString(reqPayload))
		rec := httptest.NewRecorder()
		bridge.HandleGeminiGenerateContent(rec, req, pool)
		return rec.Code, rec.Body.String()
	}

	// Loop 1
	code, resp := callProxy(`[{"role":"user","parts":[{"text":"Fix the repo bug"}]}]`)
	if code != http.StatusOK || !strings.Contains(resp, "run_command") {
		t.Fatalf("Loop 1 failed (code=%d): %s", code, resp)
	}

	// Loop 2
	code, resp = callProxy(`[
		{"role":"user","parts":[{"text":"Fix the repo bug"}]},
		{"role":"model","parts":[{"functionCall":{"name":"run_command","args":{"CommandLine":"git status"}}}]},
		{"role":"user","parts":[{"functionResponse":{"name":"run_command","response":{"output":"modified: main.go"}}}]}
	]`)
	if code != http.StatusOK || !strings.Contains(resp, "view_file") {
		t.Fatalf("Loop 2 failed (code=%d): %s", code, resp)
	}

	// Loop 3
	code, resp = callProxy(`[
		{"role":"user","parts":[{"text":"Fix the repo bug"}]},
		{"role":"model","parts":[{"functionCall":{"name":"run_command","args":{"CommandLine":"git status"}}}]},
		{"role":"user","parts":[{"functionResponse":{"name":"run_command","response":{"output":"modified: main.go"}}}]},
		{"role":"model","parts":[{"functionCall":{"name":"view_file","args":{"AbsolutePath":"/workspace/main.go"}}}]},
		{"role":"user","parts":[{"functionResponse":{"name":"view_file","response":{"content":"buggy code"}}}]}
	]`)
	if code != http.StatusOK || !strings.Contains(resp, "write_to_file") {
		t.Fatalf("Loop 3 failed (code=%d): %s", code, resp)
	}

	// Loop 4 (Final answer)
	code, resp = callProxy(`[
		{"role":"user","parts":[{"text":"Fix the repo bug"}]},
		{"role":"model","parts":[{"functionCall":{"name":"run_command","args":{"CommandLine":"git status"}}}]},
		{"role":"user","parts":[{"functionResponse":{"name":"run_command","response":{"output":"modified: main.go"}}}]},
		{"role":"model","parts":[{"functionCall":{"name":"view_file","args":{"AbsolutePath":"/workspace/main.go"}}}]},
		{"role":"user","parts":[{"functionResponse":{"name":"view_file","response":{"content":"buggy code"}}}]},
		{"role":"model","parts":[{"functionCall":{"name":"write_to_file","args":{"TargetFile":"/workspace/patch.go"}}}]},
		{"role":"user","parts":[{"functionResponse":{"name":"write_to_file","response":{"success":true}}}]}
	]`)
	if code != http.StatusOK || !strings.Contains(resp, "All changes successfully written") {
		t.Fatalf("Loop 4 failed (code=%d): %s", code, resp)
	}

	if backend.turnCount != 4 {
		t.Errorf("expected 4 turns in multi-loop agent, got %d", backend.turnCount)
	}
}

// ----------------------------------------------------------------------------
// 2. Multi Agent Test
// ----------------------------------------------------------------------------
// Verifies concurrent independent AGY agent sessions running through proxy.
func TestAGY_MultiAgent(t *testing.T) {
	backend := &mockAGYBackend{
		onTurn: func(turn int, req *types.ChatRequest) []types.StreamChunk {
			var respText string
			if strings.Contains(req.Messages[len(req.Messages)-1].Content, "Agent Alpha") {
				respText = "Response for Alpha"
			} else {
				respText = "Response for Beta"
			}
			return []types.StreamChunk{
				{ID: "chatgpt:01", Content: respText},
				{ID: "chatgpt:01", Done: true, FinishReason: "STOP"},
			}
		},
	}
	pool := router.NewAccountPoolRouter([]types.ProviderAdapter{backend})

	var wg sync.WaitGroup
	errCh := make(chan error, 10)

	for i := 0; i < 5; i++ {
		wg.Add(2)
		go func(idx int) {
			defer wg.Done()
			reqPayload := fmt.Sprintf(`{"contents":[{"role":"user","parts":[{"text":"I am Agent Alpha %d"}]}]}`, idx)
			req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-flash:generateContent", bytes.NewBufferString(reqPayload))
			rec := httptest.NewRecorder()
			bridge.HandleGeminiGenerateContent(rec, req, pool)
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Response for Alpha") {
				errCh <- fmt.Errorf("agent alpha %d failed: code=%d body=%s", idx, rec.Code, rec.Body.String())
			}
		}(i)

		go func(idx int) {
			defer wg.Done()
			reqPayload := fmt.Sprintf(`{"contents":[{"role":"user","parts":[{"text":"I am Agent Beta %d"}]}]}`, idx)
			req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-flash:generateContent", bytes.NewBufferString(reqPayload))
			rec := httptest.NewRecorder()
			bridge.HandleGeminiGenerateContent(rec, req, pool)
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Response for Beta") {
				errCh <- fmt.Errorf("agent beta %d failed: code=%d body=%s", idx, rec.Code, rec.Body.String())
			}
		}(i)
	}

	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
}

// ----------------------------------------------------------------------------
// 3. Multi Tools Test (Parallel Tool Calling)
// ----------------------------------------------------------------------------
// Model issues 3 tools simultaneously in 1 turn; AGY client responds with 3 functionResponses.
func TestAGY_MultiTools(t *testing.T) {
	backend := &mockAGYBackend{
		onTurn: func(turn int, req *types.ChatRequest) []types.StreamChunk {
			if turn == 1 {
				return []types.StreamChunk{
					{
						ID: "chatgpt:01",
						ToolCalls: []types.ToolCall{
							{ID: "call_cmd_1", Name: "run_command", Arguments: `{"CommandLine":"git status"}`},
							{ID: "call_read_1", Name: "view_file", Arguments: `{"AbsolutePath":"/workspace/README.md"}`},
							{ID: "call_grep_1", Name: "grep_search", Arguments: `{"Query":"TODO"}`},
						},
						FinishReason: "tool_calls",
						Done:         true,
					},
				}
			}
			return []types.StreamChunk{
				{ID: "chatgpt:01", Content: "All 3 parallel tools processed successfully."},
				{ID: "chatgpt:01", Done: true, FinishReason: "STOP"},
			}
		},
	}
	pool := router.NewAccountPoolRouter([]types.ProviderAdapter{backend})

	// Turn 1: Model emits 3 tool calls
	reqPayload1 := `{
		"contents": [{"role":"user","parts":[{"text":"Audit repository with multiple tools"}]}],
		"tools": [{
			"functionDeclarations": [
				{"name": "run_command"},
				{"name": "view_file"},
				{"name": "grep_search"}
			]
		}]
	}`
	req1 := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-flash:generateContent", bytes.NewBufferString(reqPayload1))
	rec1 := httptest.NewRecorder()
	bridge.HandleGeminiGenerateContent(rec1, req1, pool)

	if rec1.Code != http.StatusOK {
		t.Fatalf("Turn 1 failed: %s", rec1.Body.String())
	}

	var resp1 struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					FunctionCall *struct {
						Name string `json:"name"`
					} `json:"functionCall"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(rec1.Body.Bytes(), &resp1); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	parts := resp1.Candidates[0].Content.Parts
	if len(parts) != 3 {
		t.Fatalf("expected 3 functionCall parts, got %d", len(parts))
	}
	if parts[0].FunctionCall.Name != "run_command" ||
		parts[1].FunctionCall.Name != "view_file" ||
		parts[2].FunctionCall.Name != "grep_search" {
		t.Fatalf("unexpected parallel tool names: %+v", parts)
	}

	// Turn 2: Client returns all 3 function responses simultaneously
	reqPayload2 := `{
		"contents": [
			{"role":"user","parts":[{"text":"Audit repository with multiple tools"}]},
			{
				"role":"model",
				"parts":[
					{"functionCall":{"name":"run_command","args":{"CommandLine":"git status"}}},
					{"functionCall":{"name":"view_file","args":{"AbsolutePath":"/workspace/README.md"}}},
					{"functionCall":{"name":"grep_search","args":{"Query":"TODO"}}}
				]
			},
			{
				"role":"user",
				"parts":[
					{"functionResponse":{"name":"run_command","response":{"output":"clean working tree"}}},
					{"functionResponse":{"name":"view_file","response":{"content":"# amux project"}}},
					{"functionResponse":{"name":"grep_search","response":{"matches":["TODO: implement"]}}}
				]
			}
		],
		"tools": [{
			"functionDeclarations": [
				{"name": "run_command"},
				{"name": "view_file"},
				{"name": "grep_search"}
			]
		}]
	}`
	req2 := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-flash:generateContent", bytes.NewBufferString(reqPayload2))
	rec2 := httptest.NewRecorder()
	bridge.HandleGeminiGenerateContent(rec2, req2, pool)

	if rec2.Code != http.StatusOK || !strings.Contains(rec2.Body.String(), "All 3 parallel tools processed successfully") {
		t.Fatalf("Turn 2 failed: %s", rec2.Body.String())
	}

	// Verify FIFO ToolCallID mapping
	lastReq := backend.recordedReqs[1]
	toolMsgs := make([]types.ChatMessage, 0)
	for _, m := range lastReq.Messages {
		if strings.EqualFold(m.Role, "tool") {
			toolMsgs = append(toolMsgs, m)
		}
	}
	if len(toolMsgs) != 3 {
		t.Fatalf("expected 3 tool messages in canonical request, got %d", len(toolMsgs))
	}
	if toolMsgs[0].Name != "run_command" || toolMsgs[1].Name != "view_file" || toolMsgs[2].Name != "grep_search" {
		t.Errorf("tool message name mismatch: %+v", toolMsgs)
	}
	if toolMsgs[0].ToolCallID == "" || toolMsgs[1].ToolCallID == "" || toolMsgs[2].ToolCallID == "" {
		t.Errorf("expected non-empty synthetic tool call IDs: %+v", toolMsgs)
	}
}

// ----------------------------------------------------------------------------
// 4. Multi Sub-Agent Test
// ----------------------------------------------------------------------------
// Simulates AGY's invoke_subagent mechanism invoking 2 subagents concurrently.
func TestAGY_MultiSubAgent(t *testing.T) {
	backend := &mockAGYBackend{
		onTurn: func(turn int, req *types.ChatRequest) []types.StreamChunk {
			if turn == 1 {
				// Parent calls invoke_subagent with 2 subagents
				return []types.StreamChunk{
					{
						ID: "chatgpt:01",
						ToolCalls: []types.ToolCall{
							{
								ID:   "call_subagent_1",
								Name: "invoke_subagent",
								Arguments: `{
									"Subagents": [
										{"Role": "Research Auditor", "Prompt": "Search CVE database"},
										{"Role": "Code Linter", "Prompt": "Run golangci-lint"}
									]
								}`,
							},
						},
						FinishReason: "tool_calls",
						Done:         true,
					},
				}
			}
			return []types.StreamChunk{
				{ID: "chatgpt:01", Content: "Subagent synthesis: 0 CVEs found, linter passed cleanly."},
				{ID: "chatgpt:01", Done: true, FinishReason: "STOP"},
			}
		},
	}
	pool := router.NewAccountPoolRouter([]types.ProviderAdapter{backend})

	// Parent initiates subagents
	reqPayload := `{
		"contents": [{"role":"user","parts":[{"text":"Perform full security and lint audit with subagents"}]}],
		"tools": [{"functionDeclarations": [{"name": "invoke_subagent"}]}]
	}`
	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-flash:generateContent", bytes.NewBufferString(reqPayload))
	rec := httptest.NewRecorder()
	bridge.HandleGeminiGenerateContent(rec, req, pool)

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "invoke_subagent") {
		t.Fatalf("invoke_subagent turn failed: %s", rec.Body.String())
	}

	// Subagents return aggregated results to parent
	reqPayload2 := `{
		"contents": [
			{"role":"user","parts":[{"text":"Perform full security and lint audit with subagents"}]},
			{
				"role":"model",
				"parts":[{"functionCall":{"name":"invoke_subagent","args":{"Subagents":[{"Role":"Research Auditor"},{"Role":"Code Linter"}]}}}]
			},
			{
				"role":"user",
				"parts":[{
					"functionResponse":{
						"name":"invoke_subagent",
						"response":{
							"results":[
								{"subagent":"Research Auditor","status":"completed","findings":"No CVEs"},
								{"subagent":"Code Linter","status":"completed","findings":"All rules clean"}
							]
						}
					}
				}]
			}
		],
		"tools": [{"functionDeclarations": [{"name": "invoke_subagent"}]}]
	}`
	req2 := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-flash:generateContent", bytes.NewBufferString(reqPayload2))
	rec2 := httptest.NewRecorder()
	bridge.HandleGeminiGenerateContent(rec2, req2, pool)

	if rec2.Code != http.StatusOK || !strings.Contains(rec2.Body.String(), "Subagent synthesis: 0 CVEs found") {
		t.Fatalf("subagent completion turn failed: %s", rec2.Body.String())
	}
}

// ----------------------------------------------------------------------------
// 5. Grand Combo Test: Multi Sub-Agent + Multi Loop Agent + Multi Tools
// ----------------------------------------------------------------------------
// Complete AGY end-to-end orchestration:
// Turn 1: Parent agent invokes Subagent (invoke_subagent)
// Turn 2: Subagent launches multi-tools in parallel (find_by_name + view_file)
// Turn 3: Subagent executes next loop with run_command (go test)
// Turn 4: Subagent completes, Parent agent invokes write_to_file to persist results
// Turn 5: Parent finishes with final report
func TestAGY_Combo_SubAgent_MultiLoop_MultiTools(t *testing.T) {
	backend := &mockAGYBackend{
		onTurn: func(turn int, req *types.ChatRequest) []types.StreamChunk {
			switch turn {
			case 1:
				// Parent invokes subagent
				return []types.StreamChunk{
					{
						ID: "chatgpt:01",
						ToolCalls: []types.ToolCall{
							{ID: "call_sub_1", Name: "invoke_subagent", Arguments: `{"Subagents":[{"Role":"Test Worker","Prompt":"Execute tests and verify files"}]}`},
						},
						FinishReason: "tool_calls",
						Done:         true,
					},
				}
			case 2:
				// Subagent launches multi-tools (parallel find + read)
				return []types.StreamChunk{
					{
						ID: "chatgpt:01",
						ToolCalls: []types.ToolCall{
							{ID: "call_find_1", Name: "find_by_name", Arguments: `{"Pattern":"*_test.go"}`},
							{ID: "call_view_1", Name: "view_file", Arguments: `{"AbsolutePath":"/workspace/main_test.go"}`},
						},
						FinishReason: "tool_calls",
						Done:         true,
					},
				}
			case 3:
				// Subagent runs next loop (run_command for testing)
				return []types.StreamChunk{
					{
						ID: "chatgpt:01",
						ToolCalls: []types.ToolCall{
							{ID: "call_run_1", Name: "run_command", Arguments: `{"CommandLine":"go test ./..."}`},
						},
						FinishReason: "tool_calls",
						Done:         true,
					},
				}
			case 4:
				// Subagent done, Parent records results with write_to_file
				return []types.StreamChunk{
					{
						ID: "chatgpt:01",
						ToolCalls: []types.ToolCall{
							{ID: "call_write_1", Name: "write_to_file", Arguments: `{"TargetFile":"report.md","CodeContent":"# Test Report: 100% Passed"}`},
						},
						FinishReason: "tool_calls",
						Done:         true,
					},
				}
			default:
				// Final turn: Parent concludes
				return []types.StreamChunk{
					{ID: "chatgpt:01", Content: "Mission accomplished: Subagent finished, all tests passed, report saved to report.md."},
					{ID: "chatgpt:01", Done: true, FinishReason: "STOP"},
				}
			}
		},
	}
	pool := router.NewAccountPoolRouter([]types.ProviderAdapter{backend})

	send := func(payload string) (int, string) {
		req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-flash:generateContent", bytes.NewBufferString(payload))
		rec := httptest.NewRecorder()
		bridge.HandleGeminiGenerateContent(rec, req, pool)
		return rec.Code, rec.Body.String()
	}

	// Turn 1: Launch Subagent
	code, resp := send(`{
		"contents": [{"role":"user","parts":[{"text":"Execute full autonomous CI verification"}]}],
		"tools": [{"functionDeclarations": [{"name":"invoke_subagent"},{"name":"find_by_name"},{"name":"view_file"},{"name":"run_command"},{"name":"write_to_file"}]}]
	}`)
	if code != http.StatusOK || !strings.Contains(resp, "invoke_subagent") {
		t.Fatalf("Turn 1 (subagent launch) failed: %s", resp)
	}

	// Turn 2: Subagent multi-tools
	code, resp = send(`{
		"contents": [
			{"role":"user","parts":[{"text":"Execute full autonomous CI verification"}]},
			{"role":"model","parts":[{"functionCall":{"name":"invoke_subagent","args":{"Role":"Test Worker"}}}]},
			{"role":"user","parts":[{"functionResponse":{"name":"invoke_subagent","response":{"status":"worker started"}}}]}
		],
		"tools": [{"functionDeclarations": [{"name":"invoke_subagent"},{"name":"find_by_name"},{"name":"view_file"},{"name":"run_command"},{"name":"write_to_file"}]}]
	}`)
	if code != http.StatusOK || !strings.Contains(resp, "find_by_name") || !strings.Contains(resp, "view_file") {
		t.Fatalf("Turn 2 (multi-tool parallel) failed: %s", resp)
	}

	// Turn 3: Subagent loop with run_command
	code, resp = send(`{
		"contents": [
			{"role":"user","parts":[{"text":"Execute full autonomous CI verification"}]},
			{"role":"model","parts":[{"functionCall":{"name":"invoke_subagent","args":{"Role":"Test Worker"}}}]},
			{"role":"user","parts":[{"functionResponse":{"name":"invoke_subagent","response":{"status":"worker started"}}}]},
			{
				"role":"model",
				"parts":[
					{"functionCall":{"name":"find_by_name","args":{"Pattern":"*_test.go"}}},
					{"functionCall":{"name":"view_file","args":{"AbsolutePath":"/workspace/main_test.go"}}}
				]
			},
			{
				"role":"user",
				"parts":[
					{"functionResponse":{"name":"find_by_name","response":{"files":["main_test.go"]}}},
					{"functionResponse":{"name":"view_file","response":{"content":"func TestMain(t *testing.T){}"}}}
				]
			}
		],
		"tools": [{"functionDeclarations": [{"name":"invoke_subagent"},{"name":"find_by_name"},{"name":"view_file"},{"name":"run_command"},{"name":"write_to_file"}]}]
	}`)
	if code != http.StatusOK || !strings.Contains(resp, "run_command") {
		t.Fatalf("Turn 3 (subagent loop with command) failed: %s", resp)
	}

	// Turn 4: Write report tool call
	code, resp = send(`{
		"contents": [
			{"role":"user","parts":[{"text":"Execute full autonomous CI verification"}]},
			{"role":"model","parts":[{"functionCall":{"name":"invoke_subagent","args":{"Role":"Test Worker"}}}]},
			{"role":"user","parts":[{"functionResponse":{"name":"invoke_subagent","response":{"status":"worker started"}}}]},
			{
				"role":"model",
				"parts":[
					{"functionCall":{"name":"find_by_name","args":{"Pattern":"*_test.go"}}},
					{"functionCall":{"name":"view_file","args":{"AbsolutePath":"/workspace/main_test.go"}}}
				]
			},
			{
				"role":"user",
				"parts":[
					{"functionResponse":{"name":"find_by_name","response":{"files":["main_test.go"]}}},
					{"functionResponse":{"name":"view_file","response":{"content":"func TestMain(t *testing.T){}"}}}
				]
			},
			{"role":"model","parts":[{"functionCall":{"name":"run_command","args":{"CommandLine":"go test ./..."}}}]},
			{"role":"user","parts":[{"functionResponse":{"name":"run_command","response":{"output":"PASS\nok"}}}]}
		],
		"tools": [{"functionDeclarations": [{"name":"invoke_subagent"},{"name":"find_by_name"},{"name":"view_file"},{"name":"run_command"},{"name":"write_to_file"}]}]
	}`)
	if code != http.StatusOK || !strings.Contains(resp, "write_to_file") {
		t.Fatalf("Turn 4 (write report) failed: %s", resp)
	}

	// Turn 5: Final conclusion
	code, resp = send(`{
		"contents": [
			{"role":"user","parts":[{"text":"Execute full autonomous CI verification"}]},
			{"role":"model","parts":[{"functionCall":{"name":"invoke_subagent","args":{"Role":"Test Worker"}}}]},
			{"role":"user","parts":[{"functionResponse":{"name":"invoke_subagent","response":{"status":"worker started"}}}]},
			{
				"role":"model",
				"parts":[
					{"functionCall":{"name":"find_by_name","args":{"Pattern":"*_test.go"}}},
					{"functionCall":{"name":"view_file","args":{"AbsolutePath":"/workspace/main_test.go"}}}
				]
			},
			{
				"role":"user",
				"parts":[
					{"functionResponse":{"name":"find_by_name","response":{"files":["main_test.go"]}}},
					{"functionResponse":{"name":"view_file","response":{"content":"func TestMain(t *testing.T){}"}}}
				]
			},
			{"role":"model","parts":[{"functionCall":{"name":"run_command","args":{"CommandLine":"go test ./..."}}}]},
			{"role":"user","parts":[{"functionResponse":{"name":"run_command","response":{"output":"PASS\nok"}}}]},
			{"role":"model","parts":[{"functionCall":{"name":"write_to_file","args":{"TargetFile":"report.md"}}}]},
			{"role":"user","parts":[{"functionResponse":{"name":"write_to_file","response":{"success":true}}}]}
		],
		"tools": [{"functionDeclarations": [{"name":"invoke_subagent"},{"name":"find_by_name"},{"name":"view_file"},{"name":"run_command"},{"name":"write_to_file"}]}]
	}`)
	if code != http.StatusOK || !strings.Contains(resp, "Mission accomplished") {
		t.Fatalf("Turn 5 (final output) failed: %s", resp)
	}

	if backend.turnCount != 5 {
		t.Errorf("expected 5 turns in combo test, got %d", backend.turnCount)
	}
}
