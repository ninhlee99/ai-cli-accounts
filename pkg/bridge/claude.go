package bridge

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"amux-accounts/pkg/router"
	"amux-accounts/pkg/types"
	"amux-accounts/pkg/usage"
)

// poolAccountLabel returns a label for `am usage`'s account column when a
// request was served by the provider pool rather than a Claude OAuth
// profile: the pinned provider if one is set (`am sw <provider>`), else a
// generic marker — the actual adapter that served a given request can
// change turn to turn as the pool fails over.
func poolAccountLabel(pool *router.AccountPoolRouter) string {
	if p := pool.Preferred(); p != "" {
		return p
	}
	return "provider-pool"
}

// AnthropicMessageRequest represents the request body sent to /v1/messages by Claude Code.
type AnthropicMessageRequest struct {
	Model       string          `json:"model"`
	Messages    []json.RawMessage `json:"messages"`
	System      json.RawMessage `json:"system,omitempty"`
	MaxTokens   int             `json:"max_tokens,omitempty"`
	Stream      bool            `json:"stream,omitempty"`
	Temperature float64         `json:"temperature,omitempty"`
}

// ToChatRequest converts an Anthropic /v1/messages payload into a standardized types.ChatRequest.
func ToChatRequest(body []byte) (*types.ChatRequest, error) {
	var aReq AnthropicMessageRequest
	if err := json.Unmarshal(body, &aReq); err != nil {
		return nil, fmt.Errorf("unmarshal anthropic request: %w", err)
	}

	req := &types.ChatRequest{
		Model:       aReq.Model,
		Stream:      aReq.Stream,
		Temperature: aReq.Temperature,
		Messages:    []types.ChatMessage{},
	}

	// 1. Parse system message if present
	if len(aReq.System) > 0 {
		var sysStr string
		if err := json.Unmarshal(aReq.System, &sysStr); err == nil && sysStr != "" {
			req.Messages = append(req.Messages, types.ChatMessage{Role: "system", Content: sysStr})
		} else {
			var sysBlocks []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if err := json.Unmarshal(aReq.System, &sysBlocks); err == nil {
				var sb strings.Builder
				for _, b := range sysBlocks {
					if b.Text != "" {
						sb.WriteString(b.Text)
						sb.WriteString("\n")
					}
				}
				if sb.Len() > 0 {
					req.Messages = append(req.Messages, types.ChatMessage{Role: "system", Content: strings.TrimSpace(sb.String())})
				}
			}
		}
	}

	// 2. Parse messages array
	for _, mRaw := range aReq.Messages {
		var m struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(mRaw, &m); err != nil {
			continue
		}

		var strContent string
		if err := json.Unmarshal(m.Content, &strContent); err == nil {
			req.Messages = append(req.Messages, types.ChatMessage{Role: m.Role, Content: strContent})
			continue
		}

		// Content can be an array of blocks: [{"type": "text", "text": "..."}]
		var blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(m.Content, &blocks); err == nil {
			var sb strings.Builder
			for _, b := range blocks {
				if b.Text != "" {
					sb.WriteString(b.Text)
					sb.WriteString("\n")
				}
			}
			req.Messages = append(req.Messages, types.ChatMessage{Role: m.Role, Content: strings.TrimSpace(sb.String())})
		}
	}

	return req, nil
}

// HandleClaudeMessages handles an Anthropic /v1/messages HTTP request using the AccountPoolRouter.
func HandleClaudeMessages(w http.ResponseWriter, r *http.Request, pool *router.AccountPoolRouter, rawBody []byte) error {
	req, err := ToChatRequest(rawBody)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return err
	}

	ctx := r.Context()
	stream, err := pool.Send(ctx, req)
	if err != nil {
		http.Error(w, fmt.Sprintf("all providers failed: %v", err), http.StatusBadGateway)
		return err
	}

	msgID := fmt.Sprintf("msg_%d", time.Now().UnixNano())

	if req.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return fmt.Errorf("streaming unsupported")
		}

		// Emit message_start
		startJSON, _ := json.Marshal(map[string]any{
			"type": "message_start",
			"message": map[string]any{
				"id":            msgID,
				"type":          "message",
				"role":          "assistant",
				"content":       []any{},
				"model":         req.Model,
				"stop_reason":   nil,
				"stop_sequence": nil,
				"usage": map[string]int{
					"input_tokens":  len(req.Messages) * 10,
					"output_tokens": 1,
				},
			},
		})
		fmt.Fprintf(w, "event: message_start\ndata: %s\n\n", startJSON)

		// Emit content_block_start
		cbStart, _ := json.Marshal(map[string]any{
			"type":  "content_block_start",
			"index": 0,
			"content_block": map[string]string{
				"type": "text",
				"text": "",
			},
		})
		fmt.Fprintf(w, "event: content_block_start\ndata: %s\n\n", cbStart)
		flusher.Flush()

		var fullContent strings.Builder
		for chunk := range stream {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if chunk.Error != nil {
				errJSON, _ := json.Marshal(map[string]any{
					"type": "error",
					"error": map[string]string{
						"type":    "api_error",
						"message": chunk.Error.Error(),
					},
				})
				fmt.Fprintf(w, "event: error\ndata: %s\n\n", errJSON)
				flusher.Flush()
				return chunk.Error
			}

			if chunk.Content != "" {
				fullContent.WriteString(chunk.Content)
				deltaJSON, _ := json.Marshal(map[string]any{
					"type":  "content_block_delta",
					"index": 0,
					"delta": map[string]string{
						"type": "text_delta",
						"text": chunk.Content,
					},
				})
				fmt.Fprintf(w, "event: content_block_delta\ndata: %s\n\n", deltaJSON)
				flusher.Flush()
			}
			if chunk.Done {
				break
			}
		}

		// Emit content_block_stop
		fmt.Fprintf(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")

		inputTokens := len(req.Messages) * 10
		outputTokens := len(fullContent.String()) / 4

		// Emit message_delta
		mDelta, _ := json.Marshal(map[string]any{
			"type": "message_delta",
			"delta": map[string]any{
				"stop_reason":   "end_turn",
				"stop_sequence": nil,
			},
			"usage": map[string]int{
				"output_tokens": outputTokens,
			},
		})
		fmt.Fprintf(w, "event: message_delta\ndata: %s\n\n", mDelta)

		// Emit message_stop
		fmt.Fprintf(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		flusher.Flush()

		recordPoolUsage(r, pool, req.Model, inputTokens, outputTokens)
		return nil
	}

	// Non-streaming aggregation
	var fullContent strings.Builder
	for chunk := range stream {
		if chunk.Error != nil {
			http.Error(w, chunk.Error.Error(), http.StatusBadGateway)
			return chunk.Error
		}
		fullContent.WriteString(chunk.Content)
		if chunk.Done {
			break
		}
	}

	inputTokens := len(req.Messages) * 10
	outputTokens := len(fullContent.String()) / 4

	w.Header().Set("Content-Type", "application/json")
	respObj := map[string]any{
		"id":            msgID,
		"type":          "message",
		"role":          "assistant",
		"model":         req.Model,
		"content":       []map[string]string{{"type": "text", "text": fullContent.String()}},
		"stop_reason":   "end_turn",
		"stop_sequence": nil,
		"usage": map[string]int{
			"input_tokens":  inputTokens,
			"output_tokens": outputTokens,
		},
	}
	err = json.NewEncoder(w).Encode(respObj)
	recordPoolUsage(r, pool, req.Model, inputTokens, outputTokens)
	return err
}

// recordPoolUsage appends an `am usage` entry for a request served by the
// provider pool. Token counts here are the same length-based estimate the
// bridge already reports back to the caller in the response's "usage"
// object — not exact, but the same number Claude Code itself will have
// displayed, so `am usage` and Claude Code's own count agree even though
// neither is byte-for-byte accurate for a non-Anthropic backend.
func recordPoolUsage(r *http.Request, pool *router.AccountPoolRouter, model string, input, output int) {
	if input == 0 && output == 0 {
		return
	}
	usage.AppendUsageEntry(types.UsageEntry{
		Time:    time.Now(),
		Account: poolAccountLabel(pool),
		Model:   model,
		Project: usage.ProjectForRemoteAddr(r.RemoteAddr),
		Session: r.Header.Get("X-Claude-Code-Session-Id"),
		Input:   input,
		Output:  output,
	})
}
