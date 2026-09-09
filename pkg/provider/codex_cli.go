package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"amux-accounts/pkg/types"
)

// CodexCLIAdapter reuses the OAuth token Codex CLI already stores at
// ~/.codex/auth.json after `codex login` (or this CLI's own `am add codex`,
// which snapshots the same file) instead of asking the user to log in a
// second time. It calls the same undocumented ChatGPT-backend endpoint the
// real `codex` binary uses (see pkg/provider/config.go's LoadAccounts for
// how this adapter gets wired into the pool, and the project plan for the
// ToS/stability caveat that comes with hitting an internal API this way).
type CodexCLIAdapter struct {
	AdapterID   string // e.g. "codexcli:01" — the active codex profile's unified ID
	PriorityLvl int
	HTTPClient  *http.Client
}

const (
	codexResponsesURL  = "https://chatgpt.com/backend-api/codex/responses"
	codexOAuthTokenURL = "https://auth.openai.com/oauth/token"
	// Public client_id Codex CLI itself uses for its device/refresh OAuth
	// flow — not a secret, it's baked into the open-source codex binary.
	codexOAuthClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
	codexDefaultModel  = "gpt-5-codex"
	codexRefreshLead   = 2 * time.Minute
)

func (a *CodexCLIAdapter) ID() string    { return a.AdapterID }
func (a *CodexCLIAdapter) Priority() int { return a.PriorityLvl }

func (a *CodexCLIAdapter) client() *http.Client {
	if a.HTTPClient != nil {
		return a.HTTPClient
	}
	return defaultHTTPClient
}

// CodexAuthPath returns the path to Codex CLI's own credentials file — the
// same file `codex login` writes and refreshes, so reading/refreshing it
// here stays in sync with normal `codex` usage too.
func CodexAuthPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".codex", "auth.json")
	}
	return filepath.Join(home, ".codex", "auth.json")
}

// CodexAuthAvailable reports whether a parseable Codex CLI auth file with a
// live access token exists, without caring whether it's expired (that's
// handled/refreshed lazily inside SendMessageStream). Used by LoadAccounts
// to decide whether to add the adapter to the pool at all.
func CodexAuthAvailable() bool {
	doc, err := readCodexAuthDoc(CodexAuthPath())
	if err != nil {
		return false
	}
	return codexAuthTokenField(doc, "access_token") != ""
}

func readCodexAuthDoc(path string) (map[string]any, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	return doc, nil
}

func codexAuthTokenField(doc map[string]any, field string) string {
	tokens, _ := doc["tokens"].(map[string]any)
	if tokens == nil {
		return ""
	}
	s, _ := tokens[field].(string)
	return s
}

// writeCodexAuthTokens persists a refreshed access/refresh token pair back
// into the auth file, preserving every other field verbatim (round-tripped
// through map[string]any rather than a narrow struct) since Codex CLI's own
// file has fields this adapter doesn't need to know about.
func writeCodexAuthTokens(path string, doc map[string]any, access, refresh string) error {
	tokens, _ := doc["tokens"].(map[string]any)
	if tokens == nil {
		tokens = map[string]any{}
	}
	tokens["access_token"] = access
	if refresh != "" {
		tokens["refresh_token"] = refresh
	}
	doc["tokens"] = tokens
	doc["last_refresh"] = time.Now().UTC().Format(time.RFC3339)

	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

// decodeJWTExpSeconds pulls the "exp" claim (epoch seconds) out of a JWT
// without verifying its signature — same trust level as the rest of this
// adapter, which only ever presents the token to the issuer it came from.
func decodeJWTExpSeconds(tok string) (int64, bool) {
	parts := strings.Split(tok, ".")
	if len(parts) < 2 {
		return 0, false
	}
	seg := parts[1]
	if m := len(seg) % 4; m != 0 {
		seg += strings.Repeat("=", 4-m)
	}
	payload, err := base64.URLEncoding.DecodeString(seg)
	if err != nil {
		return 0, false
	}
	var claims struct {
		Exp float64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Exp == 0 {
		return 0, false
	}
	return int64(claims.Exp), true
}

func refreshCodexToken(ctx context.Context, client *http.Client, refreshToken string) (access, refresh string, err error) {
	body, _ := json.Marshal(map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": refreshToken,
		"client_id":     codexOAuthClientID,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, codexOAuthTokenURL, bytes.NewReader(body))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<10))
		return "", "", fmt.Errorf("status %d: %s", resp.StatusCode, bytes.TrimSpace(b))
	}
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", "", fmt.Errorf("decode refresh response: %w", err)
	}
	if out.AccessToken == "" {
		return "", "", fmt.Errorf("empty access_token in refresh response")
	}
	return out.AccessToken, out.RefreshToken, nil
}

func (a *CodexCLIAdapter) SendMessageStream(ctx context.Context, req *types.ChatRequest) (<-chan types.StreamChunk, error) {
	path := CodexAuthPath()
	doc, err := readCodexAuthDoc(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w: read %s: %v", a.AdapterID, types.ErrAuthentication, path, err)
	}

	accessToken := codexAuthTokenField(doc, "access_token")
	accountID := codexAuthTokenField(doc, "account_id")
	if accessToken == "" {
		return nil, fmt.Errorf("%s: %w: no access_token in %s", a.AdapterID, types.ErrAuthentication, path)
	}

	if exp, ok := decodeJWTExpSeconds(accessToken); ok && time.Now().Add(codexRefreshLead).After(time.Unix(exp, 0)) {
		refreshToken := codexAuthTokenField(doc, "refresh_token")
		if refreshToken == "" {
			return nil, fmt.Errorf("%s: %w: access token expired and no refresh token in %s", a.AdapterID, types.ErrAuthentication, path)
		}
		newAccess, newRefresh, rerr := refreshCodexToken(ctx, a.client(), refreshToken)
		if rerr != nil {
			return nil, fmt.Errorf("%s: refresh codex token: %w", a.AdapterID, rerr)
		}
		accessToken = newAccess
		if werr := writeCodexAuthTokens(path, doc, newAccess, newRefresh); werr != nil {
			// Non-fatal: we can still serve this request with the freshly
			// minted token even if persisting it back to disk failed.
			log.Printf("amux: could not persist refreshed codex token to %s: %v", path, werr)
		}
	}

	prompt := BuildConcatenatedPrompt(req.Messages)
	payload := map[string]any{
		"model":  codexDefaultModel,
		"input":  prompt,
		"stream": true,
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("%s: encode: %w", a.AdapterID, err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, codexResponsesURL, bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("%s: build request: %w", a.AdapterID, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Authorization", "Bearer "+accessToken)
	if accountID != "" {
		httpReq.Header.Set("chatgpt-account-id", accountID)
	}
	httpReq.Header.Set("originator", "codex_cli_rs")
	httpReq.Header.Set("OpenAI-Beta", "responses=experimental")

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
	go streamCodexResponses(ctx, a.AdapterID, resp, out)
	return out, nil
}

// streamCodexResponses parses the Responses-API SSE event shape
// (response.output_text.delta / response.completed / ...), unlike
// streamChatGPTWeb's message.content.parts shape.
func streamCodexResponses(ctx context.Context, id string, resp *http.Response, out chan<- types.StreamChunk) {
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

		var evt struct {
			Type  string `json:"type"`
			Delta string `json:"delta"`
			Error any    `json:"error"`
		}
		if err := json.Unmarshal([]byte(payload), &evt); err != nil {
			continue
		}
		if evt.Error != nil {
			sendChunk(ctx, out, types.StreamChunk{ID: id, Error: fmt.Errorf("%s: error: %v", id, evt.Error), Done: true})
			doneSent = true
			return
		}

		switch evt.Type {
		case "response.output_text.delta":
			if evt.Delta != "" {
				if !sendChunk(ctx, out, types.StreamChunk{ID: id, Content: evt.Delta}) {
					return
				}
			}
		case "response.completed", "response.failed", "response.incomplete":
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
