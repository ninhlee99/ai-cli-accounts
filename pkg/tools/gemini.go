package tools

import (
	"encoding/json"
	"fmt"
	"strings"

	"amux-accounts/pkg/types"
	"amux-accounts/pkg/utils"
)

// GeminiFunctionDeclaration is the Gemini / Antigravity functionDeclarations[] shape.
type GeminiFunctionDeclaration struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// GeminiFunctionCall is a model-emitted functionCall part.
type GeminiFunctionCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
}

// ToGeminiFunctions converts canonical defs to Gemini functionDeclarations.
func ToGeminiFunctions(defs []types.ToolDef) []GeminiFunctionDeclaration {
	out := make([]GeminiFunctionDeclaration, 0, len(defs))
	for _, d := range defs {
		out = append(out, GeminiFunctionDeclaration{
			Name:        d.Name,
			Description: d.Description,
			Parameters:  utils.NormalizeJSONSchema(d.InputSchema),
		})
	}
	return out
}

// FromGeminiFunctions maps Gemini declarations to canonical defs.
func FromGeminiFunctions(fns []GeminiFunctionDeclaration) []types.ToolDef {
	out := make([]types.ToolDef, 0, len(fns))
	for _, f := range fns {
		if strings.TrimSpace(f.Name) == "" {
			continue
		}
		out = append(out, types.ToolDef{
			Name:        f.Name,
			Description: f.Description,
			InputSchema: utils.NormalizeJSONSchema(f.Parameters),
		})
	}
	return out
}

// FromGeminiFunctionCalls maps Gemini functionCall parts to canonical.
func FromGeminiFunctionCalls(calls []GeminiFunctionCall) []types.ToolCall {
	out := make([]types.ToolCall, 0, len(calls))
	for i, c := range calls {
		args := "{}"
		if len(c.Args) > 0 && string(c.Args) != "null" {
			args = string(c.Args)
		}
		id := fmt.Sprintf("gemini_call_%d", i+1)
		out = append(out, types.ToolCall{ID: id, Name: c.Name, Arguments: args})
	}
	return out
}

// ToGeminiFunctionCalls maps canonical calls to Gemini functionCall parts.
func ToGeminiFunctionCalls(calls []types.ToolCall) []GeminiFunctionCall {
	out := make([]GeminiFunctionCall, 0, len(calls))
	for _, c := range calls {
		args := json.RawMessage(`{}`)
		if strings.TrimSpace(c.Arguments) != "" {
			args = json.RawMessage(c.Arguments)
		}
		out = append(out, GeminiFunctionCall{Name: c.Name, Args: args})
	}
	return out
}
