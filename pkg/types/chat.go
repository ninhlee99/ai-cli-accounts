// Package types holds the shared request/response shapes and the
// ProviderAdapter interface every chat provider (OpenAI-compatible API,
// DuckDuckGo AI, ChatGPT Web, ...) implements.
package types

import (
	"context"
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

// ChatMessage is one turn in a conversation.
type ChatMessage struct {
	Role    string `json:"role"` // "system", "user", "assistant"
	Content string `json:"content"`
}

// ChatRequest is provider-agnostic; each adapter translates it into
// whatever wire format its backend expects.
type ChatRequest struct {
	Model       string        `json:"model"`
	Messages    []ChatMessage `json:"messages"`
	Stream      bool          `json:"stream"`
	Temperature float64       `json:"temperature,omitempty"`
}

// StreamChunk is one piece of a streamed reply. The producer closes the
// channel after sending a chunk with Done set (or one carrying Error).
type StreamChunk struct {
	ID      string
	Content string
	Done    bool
	Error   error
}

// ProviderAdapter is implemented by every chat backend the router can
// dispatch to.
type ProviderAdapter interface {
	ID() string
	Priority() int // 1 = highest, 2, 3, ...
	SendMessageStream(ctx context.Context, req *ChatRequest) (<-chan StreamChunk, error)
}
