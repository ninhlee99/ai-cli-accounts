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

type ClaudeWebAdapter struct {
	AdapterID   string
	PriorityLvl int
	SessionKey  string
	TargetModel string
	HTTPClient  *http.Client
}

const (
	claudeWebOrganizationsURL = "https://claude.ai/api/organizations"
	claudeWebConversationBase = "https://claude.ai/api/organizations/%s/chat_conversations"
)

func (a *ClaudeWebAdapter) ID() string    { return a.AdapterID }
func (a *ClaudeWebAdapter) Priority() int { return a.PriorityLvl }

func (a *ClaudeWebAdapter) client() *http.Client {
	if a.HTTPClient != nil {
		return a.HTTPClient
	}
	return &http.Client{Timeout: 120 * time.Second}
}

func (a *ClaudeWebAdapter) SendMessageStream(ctx context.Context, req *types.ChatRequest) (<-chan types.StreamChunk, error) {
	if a.SessionKey == "" {
		return nil, fmt.Errorf("%s: %w: no sessionKey configured", a.AdapterID, types.ErrAuthentication)
	}

	orgID, err := a.getOrganizationID(ctx)
	if err != nil {
		return nil, err
	}

	convUUID, err := a.createConversation(ctx, orgID)
	if err != nil {
		return nil, err
	}

	prompt := BuildConcatenatedPrompt(req.Messages)
	model := a.TargetModel
	if model == "" {
		model = "claude-3-5-sonnet-20241022"
	}

	payloadMap := map[string]any{
		"prompt":      prompt,
		"model":       model,
		"timezone":    "Asia/Ho_Chi_Minh",
		"attachments": []any{},
		"files":       []any{},
	}
	b, err := json.Marshal(payloadMap)
	if err != nil {
		return nil, fmt.Errorf("%s: marshal: %w", a.AdapterID, err)
	}

	chatURL := fmt.Sprintf("%s/%s/completion", fmt.Sprintf(claudeWebConversationBase, orgID), convUUID)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, chatURL, bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("%s: new request: %w", a.AdapterID, err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Cookie", "sessionKey="+a.SessionKey)
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
		return nil, fmt.Errorf("%s: status %d: %s", a.AdapterID, resp.StatusCode, bytes.TrimSpace(b))
	}

	out := make(chan types.StreamChunk)
	go streamClaudeWeb(ctx, a.AdapterID, resp, out)
	return out, nil
}

func (a *ClaudeWebAdapter) getOrganizationID(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, claudeWebOrganizationsURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Cookie", "sessionKey="+a.SessionKey)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36")

	resp, err := a.client().Do(req)
	if err != nil {
		return "", fmt.Errorf("%s: get org: %w", a.AdapterID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return "", types.ErrRateLimitReached
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return "", types.ErrAuthentication
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("%s: get org returned status %d", a.AdapterID, resp.StatusCode)
	}

	var orgs []struct {
		UUID string `json:"uuid"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&orgs); err != nil {
		return "", fmt.Errorf("%s: decode orgs: %w", a.AdapterID, err)
	}
	if len(orgs) == 0 || orgs[0].UUID == "" {
		return "", fmt.Errorf("%s: no organization found", a.AdapterID)
	}
	return orgs[0].UUID, nil
}

func (a *ClaudeWebAdapter) createConversation(ctx context.Context, orgID string) (string, error) {
	url := fmt.Sprintf(claudeWebConversationBase, orgID)
	body := []byte(`{"uuid":"","name":""}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", "sessionKey="+a.SessionKey)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36")

	resp, err := a.client().Do(req)
	if err != nil {
		return "", fmt.Errorf("%s: create conv: %w", a.AdapterID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return "", types.ErrRateLimitReached
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return "", types.ErrAuthentication
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("%s: create conv returned status %d", a.AdapterID, resp.StatusCode)
	}

	var conv struct {
		UUID string `json:"uuid"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&conv); err != nil {
		return "", fmt.Errorf("%s: decode conv: %w", a.AdapterID, err)
	}
	if conv.UUID == "" {
		return "", fmt.Errorf("%s: empty conversation uuid", a.AdapterID)
	}
	return conv.UUID, nil
}

func streamClaudeWeb(ctx context.Context, id string, resp *http.Response, out chan<- types.StreamChunk) {
	defer close(out)
	defer resp.Body.Close()

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 2<<20)

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
			Completion string `json:"completion"`
			StopReason string `json:"stop_reason"`
			Error      any    `json:"error"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		if chunk.Error != nil {
			sendChunk(ctx, out, types.StreamChunk{ID: id, Error: fmt.Errorf("%s: %v", id, chunk.Error), Done: true})
			doneSent = true
			return
		}
		if chunk.Completion != "" {
			if !sendChunk(ctx, out, types.StreamChunk{ID: id, Content: chunk.Completion}) {
				return
			}
		}
		if chunk.StopReason != "" {
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
