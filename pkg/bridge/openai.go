package bridge

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"ai-cli-accounts/pkg/router"
	"ai-cli-accounts/pkg/types"
	"ai-cli-accounts/pkg/usage"
)

// HandleChatCompletions handles standard OpenAI /v1/chat/completions requests.
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

	var req types.ChatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, fmt.Sprintf("invalid json: %v", err), http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	stream, err := pool.Send(ctx, &req)
	if err != nil {
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
			if chunk.Done {
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
		recordChatUsage(r, pool, req.Model, inputTokens, len(fullContent.String())/4)
		return
	}

	// Non-streaming
	var full strings.Builder
	for chunk := range stream {
		if chunk.Error != nil {
			http.Error(w, chunk.Error.Error(), http.StatusBadGateway)
			return
		}
		full.WriteString(chunk.Content)
		if chunk.Done {
			break
		}
	}

	completionTokens := len(full.String()) / 4
	w.Header().Set("Content-Type", "application/json")
	resp := map[string]any{
		"id":      cmplID,
		"object":  "chat.completion",
		"created": now,
		"model":   req.Model,
		"choices": []map[string]any{{
			"index": 0,
			"message": map[string]string{
				"role":    "assistant",
				"content": full.String(),
			},
			"finish_reason": "stop",
		}},
		"usage": map[string]int{
			"prompt_tokens":     inputTokens,
			"completion_tokens": completionTokens,
			"total_tokens":      inputTokens + completionTokens,
		},
	}
	_ = json.NewEncoder(w).Encode(resp)
	recordChatUsage(r, pool, req.Model, inputTokens, completionTokens)
}

// recordChatUsage appends an `am usage` entry for a request served by the
// provider pool via the OpenAI-shaped /v1/chat/completions endpoint. Mirrors
// bridge.recordPoolUsage (see claude.go): same length-based token estimate
// already reported to the caller in the response's "usage" object.
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
		"gemini-2.0-flash", "gemini-1.5-pro", "gemini-1.5-flash",
		"claude-3-5-sonnet-20241022", "claude-3-haiku-20240307",
		"llama-3.3-70b-versatile", "mixtral-8x7b-32768",
	}
	data := make([]map[string]any, 0, len(models))
	for _, m := range models {
		data = append(data, map[string]any{
			"id":       m,
			"object":   "model",
			"created":  1700000000,
			"owned_by": "ai-cli-accounts",
		})
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"object": "list",
		"data":   data,
	})
}
