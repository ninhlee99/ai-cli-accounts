package tools

import "amux-accounts/pkg/types"

// ParseCursorTools extracts tool defs from a Cursor OpenAI chat.completions body.
func ParseCursorTools(body []byte) ([]types.ToolDef, error) {
	return parseOpenAITools(body)
}

// ToCursorTools converts canonical defs to Cursor/OpenAI tools[].
func ToCursorTools(defs []types.ToolDef) []openAITool {
	return toOpenAITools(defs)
}

// ToCursorToolCalls maps canonical calls to Cursor tool_calls.
func ToCursorToolCalls(calls []types.ToolCall) []OpenAIToolCall {
	return ToOpenAIToolCalls(calls)
}

// FromCursorToolCalls maps Cursor tool_calls to canonical.
func FromCursorToolCalls(calls []OpenAIToolCall) []types.ToolCall {
	return FromOpenAIToolCalls(calls)
}

// MarshalCursorChatRequest encodes req for an OpenAI-compatible upstream
// when the client dialect is Cursor.
func MarshalCursorChatRequest(req *types.ChatRequest) ([]byte, error) {
	return MarshalOpenAIChatRequest(req)
}
