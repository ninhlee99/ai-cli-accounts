package types

import (
	"context"
	"encoding/json"
	"errors"
)

var (
	// ErrRateLimitReached is returned by SendMessageStream when the
	// provider answered with a 429 (or an equivalent rate-limit signal).
	// The router treats it as "cool this adapter down and try the next
	// one" rather than a hard failure.
	ErrRateLimitReached = errors.New("rate limit reached or cooldown active")
	// ErrAuthentication is returned when the provider rejected the
	// request as unauthenticated/forbidden (bad API key, expired session
	// token, or an anti-bot challenge the adapter didn't try to solve).
	ErrAuthentication = errors.New("authentication failed")
)

// ToolDef is a provider-agnostic tool/function declaration.
type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"` // JSON Schema object
}

// ToolCall is one model-requested tool invocation.
type ToolCall struct {
	ID        string `json:"id,omitempty"`
	Name      string `json:"name"`
	Arguments string `json:"arguments,omitempty"` // JSON object as string
}

// ChatMessage is one turn in a conversation.
//
// Roles:
//   - "system", "user", "assistant" — normal chat
//   - "tool" — tool result (OpenAI-shaped); set ToolCallID
//
// Assistant turns that invoke tools carry ToolCalls; Content may still hold
// any accompanying text.
type ChatMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
}

// ChatRequest is provider-agnostic; each adapter translates it into
// whatever wire format its backend expects.
type ChatRequest struct {
	Model       string        `json:"model"`
	Messages    []ChatMessage `json:"messages"`
	Stream      bool          `json:"stream"`
	Temperature float64       `json:"temperature,omitempty"`
	Tools       []ToolDef     `json:"tools,omitempty"`
	// ToolChoice mirrors OpenAI/Anthropic tool_choice when set ("auto",
	// "none", or a named tool). Adapters that don't support it ignore it.
	ToolChoice any `json:"tool_choice,omitempty"`
	// FullContext tells web backends to flatten the entire client history
	// into one prompt (Claude Code / Codex send full turns every request)
	// instead of only the last user message on a server-side thread.
	// Not serialized on the wire — set by the Anthropic/OpenAI bridges.
	FullContext bool `json:"-"`
	// ClientDialect hints which wire format the HTTP client spoke
	// ("anthropic", "openai", "gemini"). Used by bridges when emitting.
	ClientDialect string `json:"-"`
	// Thinking enables reasoning / extended thinking mode.
	Thinking bool `json:"thinking,omitempty"`
	// ThinkingBudget specifies max reasoning tokens (e.g. 1024, 2048).
	ThinkingBudget int `json:"thinking_budget,omitempty"`
	// ReasoningEffort mirrors OpenAI reasoning_effort ("low", "medium", "high").
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	// TargetTier requests a specific model class: "flash" (fast), "pro" (heavy/reasoning), or "" (auto).
	TargetTier string `json:"target_tier,omitempty"`
}

// StreamChunk is one piece of a streamed reply. The producer closes the
// channel after sending a chunk with Done set (or one carrying Error).
type StreamChunk struct {
	ID           string
	Content      string
	Thinking     string     // reasoning/thinking tokens emitted by the model
	ToolCalls    []ToolCall // set when the model requests tool use
	FinishReason string     // "stop", "tool_calls", "end_turn", ...
	Done         bool
	Error        error
	// LogText is the raw provider reply for watch (not sent to the client).
	LogText string
}

// ProviderAdapter is implemented by every chat backend the router can
// dispatch to.
type ProviderAdapter interface {
	ID() string
	Priority() int // 1 = highest, 2, 3, ...
	SendMessageStream(ctx context.Context, req *ChatRequest) (<-chan StreamChunk, error)
}
