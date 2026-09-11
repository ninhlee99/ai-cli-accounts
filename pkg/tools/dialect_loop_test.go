package tools

import (
	"encoding/json"
	"testing"

	"amux-accounts/pkg/types"
)

// agentToolLoop simulates what each coding client does after the proxy
// returns tool calls: execute locally, then send tool results back.
// This verifies wire formats stay consistent across dialects.
func agentToolLoop(t *testing.T, dialect string) {
	t.Helper()

	// Typical agent tools: Bash / Read / Edit
	schema := json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"},"path":{"type":"string"},"old":{"type":"string"},"new":{"type":"string"}},"required":["command"]}`)
	defs := []types.ToolDef{
		{Name: "Bash", Description: "Run shell", InputSchema: schema},
		{Name: "Read", Description: "Read file", InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`)},
		{Name: "Edit", Description: "Edit file", InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"old":{"type":"string"},"new":{"type":"string"}}}`)},
	}

	calls := []types.ToolCall{
		{ID: "call_bash_1", Name: "Bash", Arguments: `{"command":"ls -la"}`},
		{ID: "call_read_1", Name: "Read", Arguments: `{"path":"main.go"}`},
	}

	switch dialect {
	case DialectClaude:
		// Client → proxy: Anthropic tools[]
		claudeTools := ToClaudeTools(defs)
		if len(claudeTools) != 3 || claudeTools[0].Name != "Bash" {
			t.Fatalf("claude tools=%+v", claudeTools)
		}
		raw, _ := json.Marshal(map[string]any{"tools": claudeTools})
		parsed, err := ParseClaudeTools(raw)
		if err != nil || len(parsed) != 3 {
			t.Fatalf("parse claude tools: %v %+v", err, parsed)
		}

		// Proxy → client: tool_use blocks (client executes locally)
		blocks := ToClaudeToolUseBlocks(calls)
		if len(blocks) != 2 || blocks[0].Type != "tool_use" || blocks[0].Name != "Bash" {
			t.Fatalf("tool_use blocks=%+v", blocks)
		}
		if string(blocks[0].Input) != `{"command":"ls -la"}` {
			t.Fatalf("bash input=%s", blocks[0].Input)
		}

		// Round-trip blocks → canonical (as if reading Anthropic SSE)
		rawBlocks := make([]map[string]json.RawMessage, len(blocks))
		for i, b := range blocks {
			bb, _ := json.Marshal(b)
			_ = json.Unmarshal(bb, &rawBlocks[i])
		}
		back := FromClaudeToolUseBlocks(rawBlocks)
		if len(back) != 2 || back[0].ID != "call_bash_1" || back[1].Name != "Read" {
			t.Fatalf("from blocks=%+v", back)
		}

		// Client sends tool_result next turn — bridge must accept
		resultBody := []byte(`{
			"model":"claude-sonnet-4-20250514",
			"tools":[{"name":"Bash","input_schema":{"type":"object","properties":{}}}],
			"messages":[{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"call_bash_1","content":"total 0\ndrwxr-xr-x"},
				{"type":"tool_result","tool_use_id":"call_read_1","content":"package main"}
			]}]
		}`)
		again, err := ParseClaudeTools(resultBody)
		if err != nil || len(again) != 1 {
			t.Fatalf("result turn tools: %v %+v", err, again)
		}

	case DialectCursor, DialectCodex:
		oaTools := toOpenAITools(defs)
		if dialect == DialectCursor {
			oaTools = ToCursorTools(defs)
		} else {
			oaTools = ToCodexTools(defs)
		}
		if len(oaTools) != 3 || oaTools[0].Type != "function" {
			t.Fatalf("%s tools=%+v", dialect, oaTools)
		}
		raw, _ := json.Marshal(map[string]any{"tools": oaTools})
		var parsed []types.ToolDef
		var err error
		if dialect == DialectCursor {
			parsed, err = ParseCursorTools(raw)
		} else {
			parsed, err = ParseCodexTools(raw)
		}
		if err != nil || len(parsed) != 3 || parsed[0].Name != "Bash" {
			t.Fatalf("parse %s: %v %+v", dialect, err, parsed)
		}

		var oaCalls []OpenAIToolCall
		if dialect == DialectCursor {
			oaCalls = ToCursorToolCalls(calls)
		} else {
			oaCalls = ToCodexToolCalls(calls)
		}
		if len(oaCalls) != 2 || oaCalls[0].Type != "function" || oaCalls[0].Function.Name != "Bash" {
			t.Fatalf("%s calls=%+v", dialect, oaCalls)
		}
		if oaCalls[0].Function.Arguments != `{"command":"ls -la"}` {
			t.Fatalf("args=%q", oaCalls[0].Function.Arguments)
		}

		var back []types.ToolCall
		if dialect == DialectCursor {
			back = FromCursorToolCalls(oaCalls)
		} else {
			back = FromCodexToolCalls(oaCalls)
		}
		if len(back) != 2 || back[0].ID != "call_bash_1" || back[1].Arguments != `{"path":"main.go"}` {
			t.Fatalf("back=%+v", back)
		}

		// OpenAI wire for next turn: role=tool messages
		req := &types.ChatRequest{
			Model: "gpt-4o",
			Tools: defs,
			Messages: []types.ChatMessage{
				{Role: "user", Content: "list and read"},
				{Role: "assistant", ToolCalls: calls},
				{Role: "tool", ToolCallID: "call_bash_1", Name: "Bash", Content: "total 0"},
				{Role: "tool", ToolCallID: "call_read_1", Name: "Read", Content: "package main"},
			},
		}
		oaReq := toOpenAIChatRequest(req)
		if len(oaReq.Tools) != 3 {
			t.Fatalf("oa tools=%d", len(oaReq.Tools))
		}
		if len(oaReq.Messages) != 4 {
			t.Fatalf("oa msgs=%d", len(oaReq.Messages))
		}
		toolMsg := oaReq.Messages[2]
		if toolMsg["role"] != "tool" || toolMsg["tool_call_id"] != "call_bash_1" {
			t.Fatalf("tool msg=%+v", toolMsg)
		}

	case DialectGemini: // Antigravity
		fns := ToGeminiFunctions(defs)
		if len(fns) != 3 || fns[0].Name != "Bash" {
			t.Fatalf("gemini fns=%+v", fns)
		}
		backDefs := FromGeminiFunctions(fns)
		if len(backDefs) != 3 || backDefs[1].Name != "Read" {
			t.Fatalf("back defs=%+v", backDefs)
		}

		gCalls := ToGeminiFunctionCalls(calls)
		if len(gCalls) != 2 || gCalls[0].Name != "Bash" {
			t.Fatalf("gCalls=%+v", gCalls)
		}
		from := FromGeminiFunctionCalls(gCalls)
		if len(from) != 2 || from[0].Name != "Bash" || from[1].Arguments != `{"path":"main.go"}` {
			t.Fatalf("from=%+v", from)
		}
		// Gemini IDs are synthesized
		if from[0].ID == "" || from[1].ID == "" {
			t.Fatal("expected synthetic gemini call ids")
		}

	default:
		t.Fatalf("unknown dialect %q", dialect)
	}
}

func TestAgentToolLoop_Claude(t *testing.T)    { agentToolLoop(t, DialectClaude) }
func TestAgentToolLoop_Cursor(t *testing.T)    { agentToolLoop(t, DialectCursor) }
func TestAgentToolLoop_Codex(t *testing.T)     { agentToolLoop(t, DialectCodex) }
func TestAgentToolLoop_Antigravity(t *testing.T) { agentToolLoop(t, DialectGemini) }

func TestCrossDialect_ClaudeToCursorCodexGemini(t *testing.T) {
	// Claude Code tools → canonical → each peer dialect → back to Claude.
	claudeBody := []byte(`{"tools":[
		{"name":"Bash","description":"shell","input_schema":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}},
		{"name":"Read","description":"read","input_schema":{"type":"object","properties":{"path":{"type":"string"}}}}
	]}`)
	defs, err := ParseClaudeTools(claudeBody)
	if err != nil || len(defs) != 2 {
		t.Fatalf("parse: %v %+v", err, defs)
	}

	cursorRaw, _ := json.Marshal(map[string]any{"tools": ToCursorTools(defs)})
	cursorDefs, err := ParseCursorTools(cursorRaw)
	if err != nil || len(cursorDefs) != 2 || cursorDefs[0].Name != "Bash" {
		t.Fatalf("cursor: %v %+v", err, cursorDefs)
	}

	codexRaw, _ := json.Marshal(map[string]any{"tools": ToCodexTools(defs)})
	codexDefs, err := ParseCodexTools(codexRaw)
	if err != nil || len(codexDefs) != 2 {
		t.Fatalf("codex: %v %+v", err, codexDefs)
	}

	geminiDefs := FromGeminiFunctions(ToGeminiFunctions(defs))
	if len(geminiDefs) != 2 || geminiDefs[1].Name != "Read" {
		t.Fatalf("gemini=%+v", geminiDefs)
	}

	// Calls survive Claude ↔ OpenAI ↔ Gemini
	calls := []types.ToolCall{{ID: "toolu_x", Name: "Bash", Arguments: `{"command":"pwd"}`}}
	viaCursor := FromCursorToolCalls(ToCursorToolCalls(calls))
	viaCodex := FromCodexToolCalls(ToCodexToolCalls(calls))
	viaGemini := FromGeminiFunctionCalls(ToGeminiFunctionCalls(calls))
	if viaCursor[0].Arguments != `{"command":"pwd"}` || viaCodex[0].Name != "Bash" || viaGemini[0].Name != "Bash" {
		t.Fatalf("cross calls cursor=%+v codex=%+v gemini=%+v", viaCursor, viaCodex, viaGemini)
	}

	claudeBlocks := ToClaudeToolUseBlocks(viaCursor)
	if claudeBlocks[0].ID != "toolu_x" || string(claudeBlocks[0].Input) != `{"command":"pwd"}` {
		t.Fatalf("back to claude=%+v", claudeBlocks)
	}
}

func TestToolResult_OpenAIMessagesShape(t *testing.T) {
	// Ensures tool results are marshalable for openai_compatible upstream
	// (needed when Claude Code tools failover to pool providers).
	req := &types.ChatRequest{
		Model: "gpt-4o",
		Tools: []types.ToolDef{{Name: "Bash", InputSchema: json.RawMessage(`{"type":"object"}`)}},
		Messages: []types.ChatMessage{
			{Role: "assistant", ToolCalls: []types.ToolCall{{ID: "1", Name: "Bash", Arguments: `{"command":"echo hi"}`}}},
			{Role: "tool", ToolCallID: "1", Name: "Bash", Content: "hi\n"},
		},
	}
	oa := toOpenAIChatRequest(req)
	raw, err := json.Marshal(oa)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(raw) {
		t.Fatal("invalid json")
	}
	var check openAIChatRequest
	if err := json.Unmarshal(raw, &check); err != nil {
		t.Fatal(err)
	}
	if len(check.Messages) != 2 {
		t.Fatalf("msgs=%d", len(check.Messages))
	}
}
