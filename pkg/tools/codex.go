package tools

import "amux-accounts/pkg/types"

// ParseCodexTools extracts tool defs from a Codex OpenAI chat.completions body.
func ParseCodexTools(body []byte) ([]types.ToolDef, error) {
	return parseOpenAITools(body)
}

// ToCodexTools converts canonical defs to Codex/OpenAI tools[].
func ToCodexTools(defs []types.ToolDef) []openAITool {
	return toOpenAITools(defs)
}

// ToCodexToolCalls maps canonical calls to Codex tool_calls.
func ToCodexToolCalls(calls []types.ToolCall) []OpenAIToolCall {
	return ToOpenAIToolCalls(calls)
}

// FromCodexToolCalls maps Codex tool_calls to canonical.
func FromCodexToolCalls(calls []OpenAIToolCall) []types.ToolCall {
	return FromOpenAIToolCalls(calls)
}

// MarshalCodexChatRequest encodes req for an OpenAI-compatible upstream
// when the client dialect is Codex (full-context agent transcripts).
func MarshalCodexChatRequest(req *types.ChatRequest) ([]byte, error) {
	return MarshalOpenAIChatRequest(req)
}
