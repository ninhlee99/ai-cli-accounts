package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"ai-cli-accounts/pkg/types"
)

// OpenAICompatibleAdapter wraps any endpoint speaking the OpenAI
// /v1/chat/completions wire protocol (GitHub Models, Google AI Studio, Groq,
// DeepSeek, vLLM, Ollama, ...).
type OpenAICompatibleAdapter struct {
	AdapterID   string
	PriorityLvl int
	BaseURL     string
	APIKey      string
	TargetModel string
	HTTPClient  *http.Client
}

func (a *OpenAICompatibleAdapter) ID() string    { return a.AdapterID }
func (a *OpenAICompatibleAdapter) Priority() int { return a.PriorityLvl }

func (a *OpenAICompatibleAdapter) client() *http.Client {
	if a.HTTPClient != nil {
		return a.HTTPClient
	}
	return &http.Client{Timeout: 120 * time.Second}
}

// SendMessageStream posts req (with Model swapped for TargetModel) to
// BaseURL+"/chat/completions" and streams the SSE reply back as
// types.StreamChunk values.
func (a *OpenAICompatibleAdapter) SendMessageStream(ctx context.Context, req *types.ChatRequest) (<-chan types.StreamChunk, error) {
	body := *req
	body.Model = a.TargetModel
	body.Stream = true

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("%s: encode request: %w", a.AdapterID, err)
	}

	baseURL := strings.TrimRight(a.BaseURL, "/")
	url := baseURL + "/chat/completions"
	if strings.HasSuffix(baseURL, "/chat/completions") {
		url = baseURL
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("%s: build request: %w", a.AdapterID, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if a.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+a.APIKey)
	}
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := a.client().Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", a.AdapterID, err)
	}

	switch resp.StatusCode {
	case http.StatusTooManyRequests:
		resp.Body.Close()
		return nil, types.ErrRateLimitReached
	case http.StatusUnauthorized, http.StatusForbidden:
		resp.Body.Close()
		return nil, types.ErrAuthentication
	}
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<10))
		resp.Body.Close()
		return nil, fmt.Errorf("%s: status %d: %s", a.AdapterID, resp.StatusCode, bytes.TrimSpace(b))
	}

	out := make(chan types.StreamChunk)
	go streamOpenAISSE(ctx, a.AdapterID, resp, out)
	return out, nil
}

func streamOpenAISSE(ctx context.Context, id string, resp *http.Response, out chan<- types.StreamChunk) {
	defer close(out)
	defer resp.Body.Close()

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	doneSent := false
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" {
			continue
		}
		if payload == "[DONE]" {
			sendChunk(ctx, out, types.StreamChunk{ID: id, Done: true})
			doneSent = true
			return
		}

		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
			Error *struct {
				Message string `json:"message"`
				Code    any    `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		if chunk.Error != nil {
			sendChunk(ctx, out, types.StreamChunk{
				ID:    id,
				Error: fmt.Errorf("%s: upstream error: %s", id, chunk.Error.Message),
				Done:  true,
			})
			doneSent = true
			return
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		choice := chunk.Choices[0]
		if choice.Delta.Content != "" {
			if !sendChunk(ctx, out, types.StreamChunk{ID: id, Content: choice.Delta.Content}) {
				return
			}
		}
		if choice.FinishReason != nil && *choice.FinishReason != "" {
			sendChunk(ctx, out, types.StreamChunk{ID: id, Done: true})
			doneSent = true
			return
		}
	}
	if err := sc.Err(); err != nil && ctx.Err() == nil {
		sendChunk(ctx, out, types.StreamChunk{ID: id, Error: fmt.Errorf("%s: read stream: %w", id, err), Done: true})
		return
	}
	if !doneSent && ctx.Err() == nil {
		sendChunk(ctx, out, types.StreamChunk{ID: id, Done: true})
	}
}
