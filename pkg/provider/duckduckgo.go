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

	"amux-accounts/pkg/types"
)

type DuckDuckGoAdapter struct {
	AdapterID   string
	PriorityLvl int
	TargetModel string // e.g. claude-3-haiku-20240307
	HTTPClient  *http.Client
}

const (
	duckduckgoStatusURL = "https://duckduckgo.com/duckchat/v1/status"
	duckduckgoChatURL   = "https://duckduckgo.com/duckchat/v1/chat"
)

func (a *DuckDuckGoAdapter) ID() string    { return a.AdapterID }
func (a *DuckDuckGoAdapter) Priority() int { return a.PriorityLvl }

func (a *DuckDuckGoAdapter) client() *http.Client {
	if a.HTTPClient != nil {
		return a.HTTPClient
	}
	// Shared, connection-pooled client — see http_client.go.
	return defaultHTTPClient
}

// SendMessageStream performs the vqd handshake, then streams the reply.
func (a *DuckDuckGoAdapter) SendMessageStream(ctx context.Context, req *types.ChatRequest) (<-chan types.StreamChunk, error) {
	vqd, err := a.handshake(ctx)
	if err != nil {
		return nil, err
	}

	// DuckDuckGo only accepts "user" and "assistant" roles.
	// Fold system instructions into the first user message so requests don't fail.
	messages := make([]types.ChatMessage, 0, len(req.Messages))
	var sysPrompt string
	for _, m := range req.Messages {
		if strings.EqualFold(m.Role, "system") {
			if sysPrompt != "" {
				sysPrompt += "\n\n"
			}
			sysPrompt += m.Content
		} else {
			messages = append(messages, m)
		}
	}
	if sysPrompt != "" {
		if len(messages) > 0 && strings.EqualFold(messages[0].Role, "user") {
			messages[0].Content = "[System Instructions]\n" + sysPrompt + "\n\n" + messages[0].Content
		} else {
			messages = append([]types.ChatMessage{{Role: "user", Content: "[System Instructions]\n" + sysPrompt}}, messages...)
		}
	}

	payload, err := json.Marshal(struct {
		Model    string              `json:"model"`
		Messages []types.ChatMessage `json:"messages"`
	}{Model: a.TargetModel, Messages: messages})
	if err != nil {
		return nil, fmt.Errorf("%s: encode request: %w", a.AdapterID, err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, duckduckgoChatURL, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("%s: build request: %w", a.AdapterID, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("x-vqd-4", vqd)

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
	go streamDuckDuckGo(ctx, a.AdapterID, resp, out)
	return out, nil
}

func (a *DuckDuckGoAdapter) handshake(ctx context.Context) (string, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, duckduckgoStatusURL, nil)
	if err != nil {
		return "", fmt.Errorf("%s: build handshake request: %w", a.AdapterID, err)
	}
	httpReq.Header.Set("x-vqd-accept", "1")

	resp, err := a.client().Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("%s: handshake: %w", a.AdapterID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return "", types.ErrRateLimitReached
	}
	vqd := resp.Header.Get("x-vqd-4")
	if resp.StatusCode >= 400 || vqd == "" {
		return "", fmt.Errorf("%s: handshake status %d, x-vqd-4=%q", a.AdapterID, resp.StatusCode, vqd)
	}
	return vqd, nil
}

func streamDuckDuckGo(ctx context.Context, id string, resp *http.Response, out chan<- types.StreamChunk) {
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

		var msg struct {
			Message string `json:"message"`
			Action  string `json:"action"`
			Type    string `json:"type"`
		}
		if err := json.Unmarshal([]byte(payload), &msg); err != nil {
			continue
		}
		if msg.Action == "error" || strings.EqualFold(msg.Type, "error") {
			sendChunk(ctx, out, types.StreamChunk{ID: id, Error: fmt.Errorf("%s: %s", id, payload), Done: true})
			doneSent = true
			return
		}
		if msg.Message == "" {
			continue
		}
		if !sendChunk(ctx, out, types.StreamChunk{ID: id, Content: msg.Message}) {
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
