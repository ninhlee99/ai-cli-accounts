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
		// Claude Code always resends the full transcript; web backends
		// must flatten it or the model only sees the last user line.
		FullContext: true,
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

		content := flattenAnthropicContent(m.Content)
		if content == "" && m.Role == "" {
			continue
		}
		req.Messages = append(req.Messages, types.ChatMessage{Role: m.Role, Content: content})
	}

	return req, nil
}

// flattenAnthropicContent turns Anthropic content (string or blocks) into
// plain text for web backends. tool_use / tool_result become readable
// context — not tool emulation; the CLI still owns real tools.
func flattenAnthropicContent(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var blocks []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return strings.TrimSpace(string(raw))
	}
	var sb strings.Builder
	for _, b := range blocks {
		var typ string
		_ = json.Unmarshal(b["type"], &typ)
		switch typ {
		case "text":
			var text string
			_ = json.Unmarshal(b["text"], &text)
			if text != "" {
				if sb.Len() > 0 {
					sb.WriteByte('\n')
				}
				sb.WriteString(text)
			}
		case "tool_use":
			var name, id string
			_ = json.Unmarshal(b["name"], &name)
			_ = json.Unmarshal(b["id"], &id)
			input := b["input"]
			if sb.Len() > 0 {
				sb.WriteByte('\n')
			}
			sb.WriteString("[Prior tool call")
			if name != "" {
				sb.WriteString(": ")
				sb.WriteString(name)
			}
			if id != "" {
				sb.WriteString(" id=")
				sb.WriteString(id)
			}
			sb.WriteString("]\n")
			if len(input) > 0 && string(input) != "null" {
				sb.WriteString(truncateRunes(string(input), 2000))
			}
		case "tool_result":
			var toolUseID string
			_ = json.Unmarshal(b["tool_use_id"], &toolUseID)
			if sb.Len() > 0 {
				sb.WriteByte('\n')
			}
			sb.WriteString("[Tool result")
			if toolUseID != "" {
				sb.WriteString(" for ")
				sb.WriteString(toolUseID)
			}
			sb.WriteString("]\n")
			sb.WriteString(truncateRunes(toolResultBody(b["content"]), 4000))
		}
	}
	return strings.TrimSpace(sb.String())
}

func toolResultBody(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	// Nested content blocks
	return flattenAnthropicContent(raw)
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
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
