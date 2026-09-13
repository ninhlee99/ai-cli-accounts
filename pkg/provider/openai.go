package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"amux-accounts/pkg/tools"
	"amux-accounts/pkg/types"
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
	// Shared, connection-pooled client — see http_client.go.
	return defaultHTTPClient
}

// SendMessageStream posts req (with Model swapped for TargetModel) to
// BaseURL+"/chat/completions" and streams the SSE reply back as
// types.StreamChunk values. Tools are converted to OpenAI function format
// via pkg/tools so Claude Code / Cursor / Codex defs round-trip.
func (a *OpenAICompatibleAdapter) SendMessageStream(ctx context.Context, req *types.ChatRequest) (<-chan types.StreamChunk, error) {
	body := *req
	body.Model = a.TargetModel

	// Auto-escalation for Google AI Studio / Gemini endpoint when heavy/analytical task is detected
	if strings.Contains(a.BaseURL, "generativelanguage.googleapis.com") {
		if strings.EqualFold(req.TargetTier, "pro") || req.Thinking {
			if strings.Contains(body.Model, "flash") || strings.Contains(body.Model, "claude") {
				body.Model = "gemini-3.1-pro-preview"
				log.Printf("%s: heavy/analytical task -> auto-switched Gemini model from %s to %s", a.AdapterID, a.TargetModel, body.Model)
			}
		}
		if body.Model == "gemini-3.1-pro" {
			body.Model = "gemini-3.1-pro-preview"
		}
	}
	body.Stream = true

	payload, err := tools.MarshalOpenAIChatRequest(&body)
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

	type deltaToolCall struct {
		Index    int    `json:"index"`
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	}

	type accCall struct {
		id, name, args string
	}
	acc := map[int]*accCall{}

	flush := func() []types.ToolCall {
		if len(acc) == 0 {
			return nil
		}
		max := -1
		for i := range acc {
			if i > max {
				max = i
			}
		}
		var outCalls []types.ToolCall
		for i := 0; i <= max; i++ {
			a := acc[i]
			if a == nil || a.name == "" {
				continue
			}
			args := a.args
			if args == "" {
				args = "{}"
			}
			outCalls = append(outCalls, types.ToolCall{ID: a.id, Name: a.name, Arguments: args})
		}
		return outCalls
	}

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
			calls := flush()
			fr := ""
			if len(calls) > 0 {
				fr = "tool_calls"
			}
			sendChunk(ctx, out, types.StreamChunk{ID: id, ToolCalls: calls, FinishReason: fr, Done: true})
			doneSent = true
			return
		}

		var chunk struct {
			Choices []struct {
				Delta struct {
					Content          string          `json:"content"`
					ReasoningContent string          `json:"reasoning_content"`
					Reasoning        string          `json:"reasoning"`
					ToolCalls        []deltaToolCall `json:"tool_calls"`
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
		reasoning := choice.Delta.ReasoningContent
		if reasoning == "" {
			reasoning = choice.Delta.Reasoning
		}
		if reasoning != "" {
			if !sendChunk(ctx, out, types.StreamChunk{ID: id, Thinking: reasoning}) {
				return
			}
		}
		if choice.Delta.Content != "" {
			if !sendChunk(ctx, out, types.StreamChunk{ID: id, Content: choice.Delta.Content}) {
				return
			}
		}
		for _, tc := range choice.Delta.ToolCalls {
			a := acc[tc.Index]
			if a == nil {
				a = &accCall{}
				acc[tc.Index] = a
			}
			if tc.ID != "" {
				a.id = tc.ID
			}
			if tc.Function.Name != "" {
				a.name = tc.Function.Name
			}
			a.args += tc.Function.Arguments
		}
		if choice.FinishReason != nil && *choice.FinishReason != "" {
			calls := flush()
			fr := *choice.FinishReason
			if len(calls) > 0 && fr == "stop" {
				fr = "tool_calls"
			}
			sendChunk(ctx, out, types.StreamChunk{ID: id, ToolCalls: calls, FinishReason: fr, Done: true})
			doneSent = true
			return
		}
	}
	if err := sc.Err(); err != nil && ctx.Err() == nil {
		sendChunk(ctx, out, types.StreamChunk{ID: id, Error: fmt.Errorf("%s: read stream: %w", id, err), Done: true})
		return
	}
	if !doneSent && ctx.Err() == nil {
		calls := flush()
		fr := ""
		if len(calls) > 0 {
			fr = "tool_calls"
		}
		sendChunk(ctx, out, types.StreamChunk{ID: id, ToolCalls: calls, FinishReason: fr, Done: true})
	}
}

