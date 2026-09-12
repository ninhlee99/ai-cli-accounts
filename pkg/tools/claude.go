package tools

import (
	"encoding/json"
	"strings"

	"amux-accounts/pkg/types"
	"amux-accounts/pkg/utils"
)

// ClaudeTool is the tools[] entry Claude Code sends on /v1/messages.
type ClaudeTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// ClaudeToolUseBlock is a content block type=tool_use.
type ClaudeToolUseBlock struct {
	Type  string          `json:"type"` // tool_use
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// ParseClaudeTools extracts tool defs from a Claude Code / Anthropic body.
func ParseClaudeTools(body []byte) ([]types.ToolDef, error) {
	var wrap struct {
		Tools []ClaudeTool `json:"tools"`
	}
	if err := json.Unmarshal(body, &wrap); err != nil {
		return nil, err
	}
	out := make([]types.ToolDef, 0, len(wrap.Tools))
	for _, t := range wrap.Tools {
		if strings.TrimSpace(t.Name) == "" {
			continue
		}
		out = append(out, types.ToolDef{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: utils.NormalizeJSONSchema(t.InputSchema),
		})
	}
	return out, nil
}

// ToClaudeTools converts canonical defs to Claude Code tools[].
func ToClaudeTools(defs []types.ToolDef) []ClaudeTool {
	out := make([]ClaudeTool, 0, len(defs))
	for _, d := range defs {
		out = append(out, ClaudeTool{
			Name:        d.Name,
			Description: d.Description,
			InputSchema: utils.NormalizeJSONSchema(d.InputSchema),
		})
	}
	return out
}

// ToClaudeToolUseBlocks converts calls to Anthropic content blocks for SSE/JSON.
func ToClaudeToolUseBlocks(calls []types.ToolCall) []ClaudeToolUseBlock {
	out := make([]ClaudeToolUseBlock, 0, len(calls))
	for _, c := range calls {
		input := json.RawMessage(`{}`)
		if strings.TrimSpace(c.Arguments) != "" && json.Valid([]byte(c.Arguments)) {
			input = json.RawMessage(c.Arguments)
		}
		out = append(out, ClaudeToolUseBlock{
			Type:  "tool_use",
			ID:    c.ID,
			Name:  c.Name,
			Input: input,
		})
	}
	return out
}

// FromClaudeToolUseBlocks extracts tool calls from Anthropic content blocks.
func FromClaudeToolUseBlocks(blocks []map[string]json.RawMessage) []types.ToolCall {
	var out []types.ToolCall
	for _, b := range blocks {
		var typ string
		_ = json.Unmarshal(b["type"], &typ)
		if typ != "tool_use" {
			continue
		}
		var id, name string
		_ = json.Unmarshal(b["id"], &id)
		_ = json.Unmarshal(b["name"], &name)
		args := "{}"
		if len(b["input"]) > 0 && string(b["input"]) != "null" {
			args = string(b["input"])
		}
		out = append(out, types.ToolCall{ID: id, Name: name, Arguments: args})
	}
	return out
}
