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

	"amux-accounts/pkg/types"
)

type ChatGPTWebAdapter struct {
	AdapterID    string
	PriorityLvl  int
	SessionToken string
	TargetModel  string
	HTTPClient   *http.Client
}

const chatGPTConversationURL = "https://chatgpt.com/backend-api/conversation"

func (a *ChatGPTWebAdapter) ID() string    { return a.AdapterID }
func (a *ChatGPTWebAdapter) Priority() int { return a.PriorityLvl }

func (a *ChatGPTWebAdapter) client() *http.Client {
	if a.HTTPClient != nil {
		return a.HTTPClient
	}
	// Shared, connection-pooled client instead of allocating a fresh
	// http.Client (and fresh TCP/TLS connections) per request. See
	// http_client.go.
	return defaultHTTPClient
}

// BuildConcatenatedPrompt flattens a multi-turn ChatRequest into the single
// text blob the web adapters (ChatGPT, Claude web) send as one message.
// Multiple system messages are merged into one "[System Instructions]"
// block up front (rather than repeating the header per message) so the
// instructions read as one coherent block and nothing is dropped.
//
// Edge case: if messages contains only system message(s) and no user/
// assistant turns (e.g. a client sends a bare system prompt with an empty
// history), the output is still well-formed: "[System Instructions]\n<...>"
// followed by a trailing "Assistant:" cue with no history in between. This
// is intentional — it still gives the web adapter's chat UI a prompt to
// respond to instead of an empty string. See TestBuildConcatenatedPrompt_
// SystemOnly in config_test.go for the locked-in behavior.
func BuildConcatenatedPrompt(messages []types.ChatMessage) string {
	if len(messages) == 0 {
		return ""
	}
	if len(messages) == 1 && strings.EqualFold(messages[0].Role, "user") {
		return messages[0].Content
	}

	var sys strings.Builder
	var sb strings.Builder
	for _, m := range messages {
		role := strings.ToLower(m.Role)
		switch role {
		case "system":
			if sys.Len() > 0 {
				sys.WriteString("\n\n")
			}
			sys.WriteString(m.Content)
		case "user":
			sb.WriteString("User: ")
			sb.WriteString(m.Content)
			sb.WriteString("\n\n")
		case "assistant":
			sb.WriteString("Assistant: ")
			sb.WriteString(m.Content)
			sb.WriteString("\n\n")
		default:
			title := role
			if len(title) > 0 {
				title = strings.ToUpper(title[:1]) + strings.ToLower(title[1:])
			}
			sb.WriteString(fmt.Sprintf("%s: %s\n\n", title, m.Content))
		}
	}

	var out strings.Builder
	if sys.Len() > 0 {
		out.WriteString("[System Instructions]\n")
		out.WriteString(sys.String())
		out.WriteString("\n\n")
	}
	out.WriteString(sb.String())
	out.WriteString("Assistant: ")
	return strings.TrimSpace(out.String())
}

func (a *ChatGPTWebAdapter) SendMessageStream(ctx context.Context, req *types.ChatRequest) (<-chan types.StreamChunk, error) {
	if a.SessionToken == "" {
		return nil, fmt.Errorf("%s: %w: no session token configured", a.AdapterID, types.ErrAuthentication)
	}

	model := a.TargetModel
	if model == "" {
		model = "auto"
	}

	combinedPrompt := BuildConcatenatedPrompt(req.Messages)
	messageID := fmt.Sprintf("msg-%d", time.Now().UnixNano())

	payloadMap := map[string]any{
		"action": "next",
		"messages": []map[string]any{
			{
				"id":     messageID,
				"author": map[string]string{"role": "user"},
				"content": map[string]any{
					"content_type": "text",
					"parts":        []string{combinedPrompt},
				},
			},
		},
		"model":             model,
		"timezone_offset_min": -420,
		"history_and_training_disabled": false,
	}

	b, err := json.Marshal(payloadMap)
	if err != nil {
		return nil, fmt.Errorf("%s: encode: %w", a.AdapterID, err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, chatGPTConversationURL, bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("%s: build request: %w", a.AdapterID, err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Authorization", "Bearer "+a.SessionToken)
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36")

	resp, err := a.client().Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", a.AdapterID, err)
	}

	if resp.StatusCode == http.StatusTooManyRequests {
		resp.Body.Close()
		return nil, types.ErrRateLimitReached
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		resp.Body.Close()
		return nil, types.ErrAuthentication
	}
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<10))
		resp.Body.Close()
		return nil, fmt.Errorf("%s: upstream status %d: %s", a.AdapterID, resp.StatusCode, bytes.TrimSpace(b))
	}

	out := make(chan types.StreamChunk)
	go streamChatGPTWeb(ctx, a.AdapterID, resp, out)
	return out, nil
}

func streamChatGPTWeb(ctx context.Context, id string, resp *http.Response, out chan<- types.StreamChunk) {
	defer close(out)
	defer resp.Body.Close()

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 2<<20)

	var lastText string
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
			Message struct {
				Content struct {
					Parts []string `json:"parts"`
				} `json:"content"`
				Status string `json:"status"`
			} `json:"message"`
			Error any `json:"error"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		if chunk.Error != nil {
			sendChunk(ctx, out, types.StreamChunk{ID: id, Error: fmt.Errorf("%s: error: %v", id, chunk.Error), Done: true})
			doneSent = true
			return
		}

		if len(chunk.Message.Content.Parts) > 0 {
			fullText := chunk.Message.Content.Parts[0]
			if strings.HasPrefix(fullText, lastText) {
				delta := fullText[len(lastText):]
				lastText = fullText
				if delta != "" {
					if !sendChunk(ctx, out, types.StreamChunk{ID: id, Content: delta}) {
						return
					}
				}
			} else {
				lastText = fullText
				if !sendChunk(ctx, out, types.StreamChunk{ID: id, Content: fullText}) {
					return
				}
			}
		}

		if chunk.Message.Status == "finished_successfully" {
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
