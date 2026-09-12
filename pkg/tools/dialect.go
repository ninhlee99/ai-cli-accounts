// Package tools is the mid-layer that speaks each coding-agent dialect
// (Claude Code, Cursor, Codex, Gemini/Antigravity) while the provider pool
// uses a single canonical types.ChatRequest.
package tools

// Dialect labels stored on types.ChatRequest.ClientDialect so bridges know
// which wire format to emit on the way back to the client.
const (
	DialectClaude = "claude" // Claude Code — Anthropic /v1/messages
	DialectCursor = "cursor" // Cursor — OpenAI /v1/chat/completions
	DialectCodex  = "codex"  // Codex — OpenAI /v1/chat/completions
	DialectGemini = "gemini" // Antigravity / Gemini function calling
)
