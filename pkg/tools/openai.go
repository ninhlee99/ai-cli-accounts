package tools

import (
	"encoding/json"
	"strings"

	"amux-accounts/pkg/types"
	"amux-accounts/pkg/utils"
)

// openAITool is the shared OpenAI tools[] wire shape (Cursor + Codex +
// openai_compatible upstream adapters).
type openAITool struct {
	Type     string `json:"type"` // "function"
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		Parameters  json.RawMessage `json:"parameters,omitempty"`
	} `json:"function"`
}

// OpenAIToolCall is the assistant.tool_calls[] wire shape.
type OpenAIToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"` // "function"
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type openAIChatRequest struct {
	Model           string           `json:"model"`
	Messages        []map[string]any `json:"messages"`
	Stream          bool             `json:"stream"`
	Temperature     float64          `json:"temperature,omitempty"`
	Tools           []openAITool     `json:"tools,omitempty"`
	ToolChoice      any              `json:"tool_choice,omitempty"`
	ReasoningEffort string           `json:"reasoning_effort,omitempty"`
}

func parseOpenAITools(body []byte) ([]types.ToolDef, error) {
	var wrap struct {
		Tools []openAITool `json:"tools"`
	}
	if err := json.Unmarshal(body, &wrap); err != nil {
		return nil, err
	}
	out := make([]types.ToolDef, 0, len(wrap.Tools))
	for _, t := range wrap.Tools {
		name := t.Function.Name
		if strings.TrimSpace(name) == "" {
			continue
		}
		out = append(out, types.ToolDef{
			Name:        name,
			Description: t.Function.Description,
			InputSchema: utils.NormalizeJSONSchema(t.Function.Parameters),
		})
	}
	return out, nil
}

func toOpenAITools(defs []types.ToolDef) []openAITool {
	out := make([]openAITool, 0, len(defs))
	for _, d := range defs {
		var t openAITool
		t.Type = "function"
		t.Function.Name = d.Name
		t.Function.Description = d.Description
		t.Function.Parameters = utils.NormalizeJSONSchema(d.InputSchema)
		out = append(out, t)
	}
	return out
}

// ToOpenAIToolCalls maps canonical calls to OpenAI tool_calls.
func ToOpenAIToolCalls(calls []types.ToolCall) []OpenAIToolCall {
	out := make([]OpenAIToolCall, 0, len(calls))
	for _, c := range calls {
		var oc OpenAIToolCall
		oc.ID = c.ID
		oc.Type = "function"
		oc.Function.Name = c.Name
		oc.Function.Arguments = c.Arguments
		if oc.Function.Arguments == "" {
			oc.Function.Arguments = "{}"
		}
		out = append(out, oc)
	}
	return out
}

// FromOpenAIToolCalls maps OpenAI tool_calls to canonical.
func FromOpenAIToolCalls(calls []OpenAIToolCall) []types.ToolCall {
	out := make([]types.ToolCall, 0, len(calls))
	for _, c := range calls {
		out = append(out, types.ToolCall{
			ID:        c.ID,
			Name:      c.Function.Name,
			Arguments: c.Function.Arguments,
		})
	}
	return out
}

func toOpenAIChatRequest(req *types.ChatRequest) *openAIChatRequest {
	out := &openAIChatRequest{
		Model:       req.Model,
		Stream:      req.Stream,
		Temperature: req.Temperature,
		ToolChoice:  req.ToolChoice,
	}
	if req.ReasoningEffort != "" {
		out.ReasoningEffort = req.ReasoningEffort
	} else if req.Thinking {
		out.ReasoningEffort = "medium"
	}
	if len(req.Tools) > 0 {
		out.Tools = toOpenAITools(req.Tools)
	}
	for _, m := range req.Messages {
		msg := map[string]any{"role": m.Role}
		switch m.Role {
		case "assistant":
			if m.Content != "" {
				msg["content"] = m.Content
			} else if len(m.ToolCalls) == 0 {
				msg["content"] = ""
			}
			if len(m.ToolCalls) > 0 {
				msg["tool_calls"] = ToOpenAIToolCalls(m.ToolCalls)
			}
		case "tool":
			msg["content"] = m.Content
			if m.ToolCallID != "" {
				msg["tool_call_id"] = m.ToolCallID
			}
			if m.Name != "" {
				msg["name"] = m.Name
			}
		default:
			msg["content"] = m.Content
		}
		out.Messages = append(out.Messages, msg)
	}
	return out
}

// MarshalOpenAIChatRequest encodes a canonical request for any
// OpenAI-compatible upstream (shared by Cursor, Codex, and pool adapters).
func MarshalOpenAIChatRequest(req *types.ChatRequest) ([]byte, error) {
	return json.Marshal(toOpenAIChatRequest(req))
}
