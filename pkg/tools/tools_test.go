package tools

import (
	"encoding/json"
	"testing"

	"amux-accounts/pkg/types"
)

func TestClaudeCursorRoundTrip(t *testing.T) {
	body := []byte(`{
		"tools":[{
			"name":"Bash",
			"description":"Run a shell command",
			"input_schema":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}
		}]
	}`)
	defs, err := ParseClaudeTools(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != 1 || defs[0].Name != "Bash" {
		t.Fatalf("defs=%+v", defs)
	}

	oa := ToCursorTools(defs)
	if len(oa) != 1 || oa[0].Type != "function" || oa[0].Function.Name != "Bash" {
		t.Fatalf("cursor=%+v", oa)
	}
	raw, _ := json.Marshal(map[string]any{"tools": oa})
	back, err := ParseCursorTools(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(back) != 1 || back[0].Name != "Bash" {
		t.Fatalf("back=%+v", back)
	}

	// Codex shares the OpenAI wire — same defs round-trip.
	codexBack, err := ParseCodexTools(raw)
	if err != nil || len(codexBack) != 1 || codexBack[0].Name != "Bash" {
		t.Fatalf("codex back=%+v err=%v", codexBack, err)
	}
}

func TestGeminiRoundTrip(t *testing.T) {
	defs := []types.ToolDef{{
		Name:        "Read",
		Description: "Read a file",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
	}}
	fns := ToGeminiFunctions(defs)
	back := FromGeminiFunctions(fns)
	if len(back) != 1 || back[0].Name != "Read" {
		t.Fatalf("back=%+v", back)
	}

	calls := []types.ToolCall{{ID: "x", Name: "Read", Arguments: `{"path":"a.go"}`}}
	gCalls := ToGeminiFunctionCalls(calls)
	from := FromGeminiFunctionCalls(gCalls)
	if len(from) != 1 || from[0].Name != "Read" || from[0].Arguments != `{"path":"a.go"}` {
		t.Fatalf("from=%+v", from)
	}
}

func TestToolCallClaudeCursor(t *testing.T) {
	calls := []types.ToolCall{{ID: "toolu_1", Name: "Bash", Arguments: `{"command":"ls"}`}}
	blocks := ToClaudeToolUseBlocks(calls)
	if len(blocks) != 1 || blocks[0].Type != "tool_use" || blocks[0].Name != "Bash" {
		t.Fatalf("blocks=%+v", blocks)
	}
	oa := ToCursorToolCalls(calls)
	back := FromCursorToolCalls(oa)
	if len(back) != 1 || back[0].ID != "toolu_1" || back[0].Arguments != `{"command":"ls"}` {
		t.Fatalf("back=%+v", back)
	}
}
