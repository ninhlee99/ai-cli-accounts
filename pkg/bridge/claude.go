package bridge

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"amux-accounts/pkg/router"
	"amux-accounts/pkg/tools"
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
	Model       string            `json:"model"`
	Messages    []json.RawMessage `json:"messages"`
	System      json.RawMessage   `json:"system,omitempty"`
	MaxTokens   int               `json:"max_tokens,omitempty"`
	Stream      bool              `json:"stream,omitempty"`
	Temperature float64           `json:"temperature,omitempty"`
	ToolChoice  any               `json:"tool_choice,omitempty"`
	Thinking    *struct {
		Type         string `json:"type"`
		BudgetTokens int    `json:"budget_tokens"`
	} `json:"thinking,omitempty"`
}

// ToChatRequest converts an Anthropic /v1/messages payload into a standardized types.ChatRequest.
// Tools and tool_use/tool_result blocks are preserved via pkg/tools so the
// provider pool (OpenAI-compatible) can round-trip them; Claude Code then
// receives native tool_use SSE and executes tools locally.
func ToChatRequest(body []byte) (*types.ChatRequest, error) {
	var aReq AnthropicMessageRequest
	if err := json.Unmarshal(body, &aReq); err != nil {
		return nil, fmt.Errorf("unmarshal anthropic request: %w", err)
	}

	req := &types.ChatRequest{
		Model:         aReq.Model,
		Stream:        aReq.Stream,
		Temperature:   aReq.Temperature,
		ToolChoice:    aReq.ToolChoice,
		Messages:      []types.ChatMessage{},
		FullContext:   true,
		ClientDialect: tools.DialectClaude,
	}

	if aReq.Thinking != nil && aReq.Thinking.Type == "enabled" {
		req.Thinking = true
		req.ThinkingBudget = aReq.Thinking.BudgetTokens
	}

	if tools, err := tools.ParseClaudeTools(body); err == nil && len(tools) > 0 {
		req.Tools = tools
	}

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

	for _, mRaw := range aReq.Messages {
		var m struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(mRaw, &m); err != nil {
			continue
		}
		req.Messages = append(req.Messages, expandAnthropicMessage(m.Role, m.Content)...)
	}

	return req, nil
}

// expandAnthropicMessage turns one Anthropic message into one or more
// canonical ChatMessages. tool_use → assistant.ToolCalls; tool_result → role=tool.
func expandAnthropicMessage(role string, raw json.RawMessage) []types.ChatMessage {
	if len(raw) == 0 || string(raw) == "null" {
		if role == "" {
			return nil
		}
		return []types.ChatMessage{{Role: role}}
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return []types.ChatMessage{{Role: role, Content: s}}
	}
	var blocks []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return []types.ChatMessage{{Role: role, Content: strings.TrimSpace(string(raw))}}
	}

	var text strings.Builder
	var toolCalls []types.ToolCall
	var out []types.ChatMessage

	flushText := func(asRole string) {
		t := strings.TrimSpace(text.String())
		text.Reset()
		if t == "" && len(toolCalls) == 0 {
			return
		}
		msg := types.ChatMessage{Role: asRole, Content: t}
		if len(toolCalls) > 0 {
			msg.ToolCalls = toolCalls
			toolCalls = nil
		}
		out = append(out, msg)
	}

	for _, b := range blocks {
		var typ string
		_ = json.Unmarshal(b["type"], &typ)
		switch typ {
		case "text":
			var t string
			_ = json.Unmarshal(b["text"], &t)
			if t != "" {
				if text.Len() > 0 {
					text.WriteByte('\n')
				}
				text.WriteString(t)
			}
		case "thinking":
			var th string
			_ = json.Unmarshal(b["thinking"], &th)
			if th != "" {
				if text.Len() > 0 {
					text.WriteByte('\n')
				}
				text.WriteString("<thinking>\n" + th + "\n</thinking>")
			}
		case "redacted_thinking":
			if text.Len() > 0 {
				text.WriteByte('\n')
			}
			text.WriteString("<thinking>[redacted]</thinking>")
		case "tool_use":
			var id, name string
			_ = json.Unmarshal(b["id"], &id)
			_ = json.Unmarshal(b["name"], &name)
			args := "{}"
			if len(b["input"]) > 0 && string(b["input"]) != "null" {
				args = string(b["input"])
			}
			toolCalls = append(toolCalls, types.ToolCall{ID: id, Name: name, Arguments: args})
		case "tool_result":
			if text.Len() > 0 || len(toolCalls) > 0 {
				flushText(role)
			}
			var toolUseID string
			_ = json.Unmarshal(b["tool_use_id"], &toolUseID)
			out = append(out, types.ChatMessage{
				Role:       "tool",
				ToolCallID: toolUseID,
				Content:    toolResultBody(b["content"]),
			})
		}
	}
	if text.Len() > 0 || len(toolCalls) > 0 {
		asRole := role
		if len(toolCalls) > 0 {
			asRole = "assistant"
		}
		flushText(asRole)
	}
	if len(out) == 0 {
		flat := flattenAnthropicContent(raw)
		if flat != "" || role != "" {
			out = append(out, types.ChatMessage{Role: role, Content: flat})
		}
	}
	return out
}

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
		logChatRequest(r, pool, nil, "", "error", err.Error(), 0, 0, time.Now(), nil)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return err
	}

	started := time.Now()
	msgID := fmt.Sprintf("msg_%d", time.Now().UnixNano())

	// Stream: flush message_start before the upstream call so Claude Code
	// does not sit on "Waiting for API response / check your network" during
	// ChatGPT sentinel + PoW + TTFB (often 30–90s).
	var flusher http.Flusher
	if req.Stream {
		var ferr error
		flusher, ferr = beginAnthropicSSE(w, req, msgID)
		if ferr != nil {
			return ferr
		}
	}

	stream, err := poolSend(r, pool, req)
	if err != nil {
		logChatRequest(r, pool, req, "", "", err.Error(), 0, 0, started, nil)
		if req.Stream && flusher != nil {
			writeAnthropicSSEError(w, flusher, err)
			return err
		}
		http.Error(w, fmt.Sprintf("all providers failed: %v", err), http.StatusBadGateway)
		return err
	}

	if req.Stream {
		return writeAnthropicSSE(w, flusher, r, pool, req, stream, msgID, started)
	}

	var fullContent strings.Builder
	var thinkingContent strings.Builder
	var toolCalls []types.ToolCall
	var logText string
	finishReason := "end_turn"
	for chunk := range stream {
		if chunk.Error != nil {
			http.Error(w, chunk.Error.Error(), http.StatusBadGateway)
			return chunk.Error
		}
		if chunk.Thinking != "" {
			thinkingContent.WriteString(chunk.Thinking)
		}
		fullContent.WriteString(chunk.Content)
		if chunk.LogText != "" {
			logText = chunk.LogText
		}
		if len(chunk.ToolCalls) > 0 {
			toolCalls = append(toolCalls, chunk.ToolCalls...)
		}
		if chunk.FinishReason != "" {
			finishReason = mapFinishReasonAnthropic(chunk.FinishReason)
		}
		if chunk.Done {
			break
		}
	}
	// Deduplicate tool calls by ID if present
	if len(toolCalls) > 0 {
		seen := map[string]bool{}
		var unique []types.ToolCall
		for _, tc := range toolCalls {
			if tc.ID != "" && seen[tc.ID] {
				continue
			}
			if tc.ID != "" {
				seen[tc.ID] = true
			}
			unique = append(unique, tc)
		}
		toolCalls = unique
		finishReason = "tool_use"
	}

	inputTokens := estimateInputTokens(req)
	outputTokens := estimateStringTokens(fullContent.String()) + estimateStringTokens(thinkingContent.String())
	if outputTokens < 1 {
		outputTokens = 1
	}

	content := []any{}
	if thinkingContent.Len() > 0 {
		content = append(content, map[string]string{"type": "thinking", "thinking": thinkingContent.String()})
	}
	if fullContent.Len() > 0 {
		content = append(content, map[string]string{"type": "text", "text": fullContent.String()})
	}
	for _, b := range tools.ToClaudeToolUseBlocks(toolCalls) {
		content = append(content, b)
	}
	if len(content) == 0 {
		content = append(content, map[string]string{"type": "text", "text": ""})
	}

	w.Header().Set("Content-Type", "application/json")
	respObj := map[string]any{
		"id":            msgID,
		"type":          "message",
		"role":          "assistant",
		"model":         req.Model,
		"content":       content,
		"stop_reason":   finishReason,
		"stop_sequence": nil,
		"usage": map[string]int{
			"input_tokens":  inputTokens,
			"output_tokens": outputTokens,
		},
	}
	err = json.NewEncoder(w).Encode(respObj)
	recordPoolUsage(r, pool, req.Model, inputTokens, outputTokens)
	logChatRequest(r, pool, req, pickLogOutput(fullContent.String(), logText), finishReason, "", inputTokens, outputTokens, started, toolCalls)
	return err
}

// EstimateStringTokens computes a realistic BPE token count approximation.
func EstimateStringTokens(s string) int {
	return estimateStringTokens(s)
}

// EstimateInputTokens estimates token usage for a chat request.
func EstimateInputTokens(req *types.ChatRequest) int {
	return estimateInputTokens(req)
}

// EstimateBytesTokens is the exported wrapper for estimateBytesTokens.
// It produces the same result as EstimateStringTokens(string(b)) without the allocation.
func EstimateBytesTokens(b []byte) int {
	return estimateBytesTokens(b)
}

// estimateStringTokens computes a realistic BPE token count approximation
// for ASCII, multi-byte UTF-8 (Vietnamese, CJK), and symbols.
func estimateStringTokens(s string) int {
	if s == "" {
		return 0
	}
	tokens := 0
	asciiChars := 0
	for _, r := range s {
		if r < 128 {
			asciiChars++
		} else {
			// Multi-byte runes (e.g. Vietnamese accented letters, CJK characters, emojis)
			// BPE tokenizers typically tokenize these into 1 to 1.5 tokens each.
			tokens++
		}
	}
	tokens += (asciiChars + 3) / 4
	return tokens
}

// estimateBytesTokens is like estimateStringTokens but avoids the []byte→string
// allocation for callers that already have a []byte (e.g. json.RawMessage schemas).
// Uses utf8.DecodeRune for zero-alloc decoding that safely handles invalid/corrupted sequences.
func estimateBytesTokens(b []byte) int {
	if len(b) == 0 {
		return 0
	}
	tokens := 0
	asciiChars := 0
	i := 0
	for i < len(b) {
		if b[i] < 0x80 {
			asciiChars++
			i++
		} else {
			// Multi-byte UTF-8 sequence or invalid byte.
			// utf8.DecodeRune safely returns RuneError and size 1 on invalid bytes.
			_, size := utf8.DecodeRune(b[i:])
			tokens++
			i += size
		}
	}
	tokens += (asciiChars + 3) / 4
	return tokens
}

func estimateInputTokens(req *types.ChatRequest) int {
	if req == nil {
		return 0
	}
	// System prompt is always in Messages[0] with Role=="system" (injected by
	// ToChatRequest / geminiBodyToChatRequest), so it is already counted below
	// — there is no separate req.System field in ChatRequest.
	tokens := 3 // envelope overhead
	for _, m := range req.Messages {
		tokens += 4 // message role/formatting overhead
		tokens += estimateStringTokens(m.Content)
		for _, tc := range m.ToolCalls {
			tokens += estimateStringTokens(tc.Name) + estimateStringTokens(tc.Arguments) + 4
		}
	}
	for _, t := range req.Tools {
		tokens += 8 // tool schema envelope overhead
		tokens += estimateStringTokens(t.Name) + estimateStringTokens(t.Description) + estimateBytesTokens(t.InputSchema)
	}
	if tokens < 1 {
		tokens = 1
	}
	return tokens
}

func beginAnthropicSSE(w http.ResponseWriter, req *types.ChatRequest, msgID string) (http.Flusher, error) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return nil, fmt.Errorf("streaming unsupported")
	}

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
				"input_tokens":  estimateInputTokens(req),
				"output_tokens": 1,
			},
		},
	})
	fmt.Fprintf(w, "event: message_start\ndata: %s\n\n", startJSON)
	fmt.Fprintf(w, "event: ping\ndata: {\"type\":\"ping\"}\n\n")
	flusher.Flush()
	return flusher, nil
}

func writeAnthropicSSEError(w http.ResponseWriter, flusher http.Flusher, err error) {
	errJSON, _ := json.Marshal(map[string]any{
		"type": "error",
		"error": map[string]string{
			"type":    "api_error",
			"message": err.Error(),
		},
	})
	fmt.Fprintf(w, "event: error\ndata: %s\n\n", errJSON)
	flusher.Flush()
}

func writeAnthropicSSEPing(w http.ResponseWriter, flusher http.Flusher) {
	fmt.Fprintf(w, "event: ping\ndata: {\"type\":\"ping\"}\n\n")
	flusher.Flush()
}

func writeAnthropicSSE(w http.ResponseWriter, flusher http.Flusher, r *http.Request, pool *router.AccountPoolRouter, req *types.ChatRequest, stream <-chan types.StreamChunk, msgID string, started time.Time) error {
	ctx := r.Context()

	var fullContent strings.Builder
	var thinkingContent strings.Builder
	var toolCalls []types.ToolCall
	var logText string
	finishReason := "end_turn"
	textStarted := false
	thinkingStarted := false
	thinkingClosed := false
	blockIndex := 0

	closeThinkingBlock := func() {
		if thinkingStarted && !thinkingClosed {
			fmt.Fprintf(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":%d}\n\n", blockIndex)
			flusher.Flush()
			thinkingClosed = true
			blockIndex++
		}
	}

	ensureTextBlock := func() {
		closeThinkingBlock()
		if textStarted {
			return
		}
		cbStart, _ := json.Marshal(map[string]any{
			"type":  "content_block_start",
			"index": blockIndex,
			"content_block": map[string]string{
				"type": "text",
				"text": "",
			},
		})
		fmt.Fprintf(w, "event: content_block_start\ndata: %s\n\n", cbStart)
		flusher.Flush()
		textStarted = true
	}

	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()

loop:
	for {
		var chunk types.StreamChunk
		var ok bool
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ping.C:
			writeAnthropicSSEPing(w, flusher)
			continue
		case chunk, ok = <-stream:
			if !ok {
				break loop
			}
		}
		if chunk.Error != nil {
			writeAnthropicSSEError(w, flusher, chunk.Error)
			return chunk.Error
		}

		if chunk.LogText != "" {
			logText = chunk.LogText
		}
		if chunk.Thinking != "" {
			if !thinkingStarted {
				cbStart, _ := json.Marshal(map[string]any{
					"type":  "content_block_start",
					"index": blockIndex,
					"content_block": map[string]string{
						"type":     "thinking",
						"thinking": "",
					},
				})
				fmt.Fprintf(w, "event: content_block_start\ndata: %s\n\n", cbStart)
				flusher.Flush()
				thinkingStarted = true
			}
			thinkingContent.WriteString(chunk.Thinking)
			deltaJSON, _ := json.Marshal(map[string]any{
				"type":  "content_block_delta",
				"index": blockIndex,
				"delta": map[string]string{
					"type":     "thinking_delta",
					"thinking": chunk.Thinking,
				},
			})
			fmt.Fprintf(w, "event: content_block_delta\ndata: %s\n\n", deltaJSON)
			flusher.Flush()
		}
		if chunk.Content != "" {
			ensureTextBlock()
			fullContent.WriteString(chunk.Content)
			deltaJSON, _ := json.Marshal(map[string]any{
				"type":  "content_block_delta",
				"index": blockIndex,
				"delta": map[string]string{
					"type": "text_delta",
					"text": chunk.Content,
				},
			})
			fmt.Fprintf(w, "event: content_block_delta\ndata: %s\n\n", deltaJSON)
			flusher.Flush()
		}
		if len(chunk.ToolCalls) > 0 {
			toolCalls = append(toolCalls, chunk.ToolCalls...)
		}
		if chunk.FinishReason != "" {
			finishReason = mapFinishReasonAnthropic(chunk.FinishReason)
		}
		if chunk.Done {
			break
		}
	}

	closeThinkingBlock()
	if textStarted {
		fmt.Fprintf(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":%d}\n\n", blockIndex)
		blockIndex++
	}

	if len(toolCalls) > 0 {
		seen := map[string]bool{}
		var unique []types.ToolCall
		for _, tc := range toolCalls {
			if tc.ID != "" && seen[tc.ID] {
				continue
			}
			if tc.ID != "" {
				seen[tc.ID] = true
			}
			unique = append(unique, tc)
		}
		toolCalls = unique
		finishReason = "tool_use"
		for _, call := range toolCalls {
			if call.ID == "" {
				call.ID = fmt.Sprintf("toolu_%d", time.Now().UnixNano())
			}
			input := json.RawMessage(`{}`)
			if strings.TrimSpace(call.Arguments) != "" && json.Valid([]byte(call.Arguments)) {
				input = json.RawMessage(call.Arguments)
			}
			cbStart, _ := json.Marshal(map[string]any{
				"type":  "content_block_start",
				"index": blockIndex,
				"content_block": map[string]any{
					"type":  "tool_use",
					"id":    call.ID,
					"name":  call.Name,
					"input": map[string]any{},
				},
			})
			fmt.Fprintf(w, "event: content_block_start\ndata: %s\n\n", cbStart)

			deltaJSON, _ := json.Marshal(map[string]any{
				"type":  "content_block_delta",
				"index": blockIndex,
				"delta": map[string]any{
					"type":         "input_json_delta",
					"partial_json": string(input),
				},
			})
			fmt.Fprintf(w, "event: content_block_delta\ndata: %s\n\n", deltaJSON)
			fmt.Fprintf(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":%d}\n\n", blockIndex)
			blockIndex++
			flusher.Flush()
		}
	} else if !textStarted {
		ensureTextBlock()
		fmt.Fprintf(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":%d}\n\n", blockIndex)
	}

	inputTokens := estimateInputTokens(req)
	outputTokens := estimateStringTokens(fullContent.String()) + estimateStringTokens(thinkingContent.String())
	if outputTokens < 1 {
		outputTokens = 1
	}

	mDelta, _ := json.Marshal(map[string]any{
		"type": "message_delta",
		"delta": map[string]any{
			"stop_reason":   finishReason,
			"stop_sequence": nil,
		},
		"usage": map[string]int{
			"output_tokens": outputTokens,
		},
	})
	fmt.Fprintf(w, "event: message_delta\ndata: %s\n\n", mDelta)
	fmt.Fprintf(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	flusher.Flush()

	recordPoolUsage(r, pool, req.Model, inputTokens, outputTokens)
	logChatRequest(r, pool, req, pickLogOutput(fullContent.String(), logText), finishReason, "", inputTokens, outputTokens, started, toolCalls)
	return nil
}

func mapFinishReasonAnthropic(fr string) string {
	switch strings.ToLower(fr) {
	case "tool_calls", "tool_use", "function_call":
		return "tool_use"
	case "length", "max_tokens":
		return "max_tokens"
	default:
		return "end_turn"
	}
}

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
