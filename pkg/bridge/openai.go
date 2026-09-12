package bridge

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"amux-accounts/pkg/privacy"
	"amux-accounts/pkg/router"
	"amux-accounts/pkg/tools"
	"amux-accounts/pkg/types"
	"amux-accounts/pkg/usage"
)

// HandleChatCompletions handles standard OpenAI /v1/chat/completions requests
// from Cursor, Codex, and other OpenAI-shaped clients. Tools are normalized
// through pkg/tools so the same pool adapters serve Claude Code and Cursor.
func HandleChatCompletions(w http.ResponseWriter, r *http.Request, pool *router.AccountPoolRouter) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 10<<20))
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}
	if scrubbed, res := privacy.ScrubBytes(body); res.Len() > 0 {
		body = scrubbed
		privacy.LogHits(r, res, "openai")
	}

	req, err := openAIBodyToChatRequest(body)
	if err != nil {
		http.Error(w, fmt.Sprintf("invalid json: %v", err), http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	started := time.Now()
	stream, err := poolSend(r, pool, req)
	if err != nil {
		logChatRequest(r, pool, req, "", "", err.Error(), 0, 0, started, nil)
		http.Error(w, fmt.Sprintf("all providers failed: %v", err), http.StatusBadGateway)
		return
	}

	cmplID := fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	now := time.Now().Unix()
	inputTokens := len(req.Messages) * 10

	if req.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		var fullContent strings.Builder
		var toolCalls []types.ToolCall
		finishReason := "stop"
		stopSent := false
		for chunk := range stream {
			if ctx.Err() != nil {
				return
			}
			if chunk.Error != nil {
				errJSON, _ := json.Marshal(map[string]any{
					"error": map[string]string{"message": chunk.Error.Error()},
				})
				fmt.Fprintf(w, "data: %s\n\n", errJSON)
				flusher.Flush()
				return
			}

			if chunk.Content != "" {
				fullContent.WriteString(chunk.Content)
				chunkJSON, _ := json.Marshal(map[string]any{
					"id":      cmplID,
					"object":  "chat.completion.chunk",
					"created": now,
					"model":   req.Model,
					"choices": []map[string]any{{
						"index": 0,
						"delta": map[string]string{
							"content": chunk.Content,
						},
						"finish_reason": nil,
					}},
				})
				fmt.Fprintf(w, "data: %s\n\n", chunkJSON)
				flusher.Flush()
			}
			if len(chunk.ToolCalls) > 0 {
				toolCalls = chunk.ToolCalls
			}
			if chunk.FinishReason != "" {
				finishReason = chunk.FinishReason
			}
			if chunk.Done {
				if len(toolCalls) > 0 {
					finishReason = "tool_calls"
					oaCalls := tools.ToOpenAIToolCalls(toolCalls)
					for i, oc := range oaCalls {
						delta := map[string]any{
							"tool_calls": []map[string]any{{
								"index": i,
								"id":    oc.ID,
								"type":  "function",
								"function": map[string]string{
									"name":      oc.Function.Name,
									"arguments": oc.Function.Arguments,
								},
							}},
						}
						chunkJSON, _ := json.Marshal(map[string]any{
							"id":      cmplID,
							"object":  "chat.completion.chunk",
							"created": now,
							"model":   req.Model,
							"choices": []map[string]any{{
								"index":         0,
								"delta":         delta,
								"finish_reason": nil,
							}},
						})
						fmt.Fprintf(w, "data: %s\n\n", chunkJSON)
						flusher.Flush()
					}
				}
				stopJSON, _ := json.Marshal(map[string]any{
					"id":      cmplID,
					"object":  "chat.completion.chunk",
					"created": now,
					"model":   req.Model,
					"choices": []map[string]any{{
						"index":         0,
						"delta":         map[string]string{},
						"finish_reason": finishReason,
					}},
				})
				fmt.Fprintf(w, "data: %s\n\n", stopJSON)
				flusher.Flush()
				stopSent = true
				break
			}
		}

		if !stopSent && ctx.Err() == nil {
			stopJSON, _ := json.Marshal(map[string]any{
				"id":      cmplID,
				"object":  "chat.completion.chunk",
				"created": now,
				"model":   req.Model,
				"choices": []map[string]any{{
					"index":         0,
					"delta":         map[string]string{},
					"finish_reason": "stop",
				}},
			})
			fmt.Fprintf(w, "data: %s\n\n", stopJSON)
			flusher.Flush()
		}

		fmt.Fprintf(w, "data: [DONE]\n\n")
		flusher.Flush()
		outTok := len(fullContent.String()) / 4
		recordChatUsage(r, pool, req.Model, inputTokens, outTok)
		logChatRequest(r, pool, req, fullContent.String(), finishReason, "", inputTokens, outTok, started, toolCalls)
		return
	}

	// Non-streaming
	var full strings.Builder
	var toolCalls []types.ToolCall
	finishReason := "stop"
	for chunk := range stream {
		if chunk.Error != nil {
			http.Error(w, chunk.Error.Error(), http.StatusBadGateway)
			return
		}
		full.WriteString(chunk.Content)
		if len(chunk.ToolCalls) > 0 {
			toolCalls = chunk.ToolCalls
		}
		if chunk.FinishReason != "" {
			finishReason = chunk.FinishReason
		}
		if chunk.Done {
			break
		}
	}
	if len(toolCalls) > 0 {
		finishReason = "tool_calls"
	}

	completionTokens := len(full.String()) / 4
	msg := map[string]any{
		"role":    "assistant",
		"content": full.String(),
	}
	if len(toolCalls) > 0 {
		msg["tool_calls"] = tools.ToOpenAIToolCalls(toolCalls)
		if full.Len() == 0 {
			msg["content"] = nil
		}
	}

	w.Header().Set("Content-Type", "application/json")
	resp := map[string]any{
		"id":      cmplID,
		"object":  "chat.completion",
		"created": now,
		"model":   req.Model,
		"choices": []map[string]any{{
			"index":         0,
			"message":       msg,
			"finish_reason": finishReason,
		}},
		"usage": map[string]int{
			"prompt_tokens":     inputTokens,
			"completion_tokens": completionTokens,
			"total_tokens":      inputTokens + completionTokens,
		},
	}
	_ = json.NewEncoder(w).Encode(resp)
	recordChatUsage(r, pool, req.Model, inputTokens, completionTokens)
	logChatRequest(r, pool, req, full.String(), finishReason, "", inputTokens, completionTokens, started, toolCalls)
}

// openAIBodyToChatRequest parses a Cursor/Codex OpenAI chat.completions body
// into the canonical ChatRequest (tools use function.parameters on the wire).
func openAIBodyToChatRequest(body []byte) (*types.ChatRequest, error) {
	var wrap struct {
		Model       string  `json:"model"`
		Stream      bool    `json:"stream"`
		Temperature float64 `json:"temperature"`
		ToolChoice  any     `json:"tool_choice"`
		Messages    []struct {
			Role       string                 `json:"role"`
			Content    json.RawMessage        `json:"content"`
			Name       string                 `json:"name"`
			ToolCallID string                 `json:"tool_call_id"`
			ToolCalls  []tools.OpenAIToolCall `json:"tool_calls"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &wrap); err != nil {
		return nil, err
	}

	req := &types.ChatRequest{
		Model:         wrap.Model,
		Stream:        wrap.Stream,
		Temperature:   wrap.Temperature,
		ToolChoice:    wrap.ToolChoice,
		FullContext:   true,
		ClientDialect: tools.DialectCursor,
	}
	if tools, err := tools.ParseCursorTools(body); err == nil {
		req.Tools = tools
	}

	for _, m := range wrap.Messages {
		msg := types.ChatMessage{
			Role:       m.Role,
			Name:       m.Name,
			ToolCallID: m.ToolCallID,
			Content:    openAIContentString(m.Content),
		}
		if len(m.ToolCalls) > 0 {
			msg.ToolCalls = tools.FromOpenAIToolCalls(m.ToolCalls)
		}
		req.Messages = append(req.Messages, msg)
	}
	return req, nil
}

func openAIContentString(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	// Multimodal content array — keep text parts only.
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err == nil {
		var sb strings.Builder
		for _, p := range parts {
			if p.Type == "text" && p.Text != "" {
				if sb.Len() > 0 {
					sb.WriteByte('\n')
				}
				sb.WriteString(p.Text)
			}
		}
		return sb.String()
	}
	return strings.TrimSpace(string(raw))
}

func recordChatUsage(r *http.Request, pool *router.AccountPoolRouter, model string, input, output int) {
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

// HandleModels returns standard OpenAI-format models list.
func HandleModels(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	models := []string{
		"gpt-4o", "gpt-4o-mini", "o1", "o1-mini",
		"gemini-3.6-flash", "gemini-2.5-pro", "gemini-2.5-flash",
		"claude-3-5-sonnet-20241022", "claude-3-haiku-20240307",
		"llama-3.3-70b-versatile", "mixtral-8x7b-32768",
	}
	data := make([]map[string]any, 0, len(models))
	for _, m := range models {
		data = append(data, map[string]any{
			"id":       m,
			"object":   "model",
			"created":  1700000000,
			"owned_by": "amux-accounts",
		})
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"object": "list",
		"data":   data,
	})
}
