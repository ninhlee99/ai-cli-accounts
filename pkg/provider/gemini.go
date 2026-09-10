package provider

import (
	"context"
	"fmt"
	"net/http"

	"amux-accounts/pkg/types"
)

const googleAIStudioBaseURL = "https://generativelanguage.googleapis.com/v1beta/openai"

// GeminiAdapter wraps Google AI Studio's OpenAI-compatible endpoint.
type GeminiAdapter struct {
	AdapterID   string
	PriorityLvl int
	APIKey      string
	TargetModel string // default: gemini-3.6-flash
	HTTPClient  *http.Client

	wrapped types.ProviderAdapter
}

func NewGeminiAdapter(id string, priority int, apiKey, model string) *GeminiAdapter {
	if model == "" {
		model = "gemini-3.6-flash"
	}
	if id == "" {
		id = "google-ai-studio"
	}
	return &GeminiAdapter{
		AdapterID:   id,
		PriorityLvl: priority,
		APIKey:      apiKey,
		TargetModel: model,
		wrapped: &OpenAICompatibleAdapter{
			AdapterID:   id,
			PriorityLvl: priority,
			BaseURL:     googleAIStudioBaseURL,
			APIKey:      apiKey,
			TargetModel: model,
		},
	}
}

func (a *GeminiAdapter) ID() string    { return a.AdapterID }
func (a *GeminiAdapter) Priority() int { return a.PriorityLvl }

func (a *GeminiAdapter) SendMessageStream(ctx context.Context, req *types.ChatRequest) (<-chan types.StreamChunk, error) {
	if a.APIKey == "" {
		return nil, fmt.Errorf("%s: %w: empty API key", a.AdapterID, types.ErrAuthentication)
	}
	return a.wrapped.SendMessageStream(ctx, req)
}
