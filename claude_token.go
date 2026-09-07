package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"syscall"
	"time"
)

// Claude Code stores its OAuth token as JSON under key "claudeAiOauth" inside
// the keychain item "Claude Code-credentials" (and mirrors config in ~/.claude.json).
// The public client_id below is the one Claude Code itself uses for the PKCE flow.
const claudeOAuthClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
const claudeTokenURL = "https://console.anthropic.com/v1/oauth/token"

type claudeCreds struct {
	ClaudeAiOauth struct {
		AccessToken           string `json:"accessToken"`
		RefreshToken          string `json:"refreshToken"`
		ExpiresAt             int64  `json:"expiresAt"` // epoch millis
		RefreshTokenExpiresAt int64  `json:"refreshTokenExpiresAt"`
		Scopes                []string `json:"scopes"`
		SubscriptionType      string `json:"subscriptionType"`
		RateLimitTier         string `json:"rateLimitTier"`
	} `json:"claudeAiOauth"`
	MCPOAuth json.RawMessage `json:"mcpOAuth,omitempty"`
}

// loadClaudeToken reads the token embedded in a saved profile bundle.
func loadClaudeToken(tool, name string) *token {
	for _, e := range loadProfileEntries(tool, name) {
		if e.Artifact.Kind != "keychain" || e.Artifact.Service != "Claude Code-credentials" {
			continue
		}
		var c claudeCreds
		if json.Unmarshal(e.Data, &c) != nil {
			continue
		}
		o := c.ClaudeAiOauth
		return &token{
			Access:    o.AccessToken,
			Refresh:   o.RefreshToken,
			ExpiresAt: time.UnixMilli(o.ExpiresAt),
			remaining: -1,
		}
	}
	return &token{remaining: -1}
}

// persistClaudeToken writes a refreshed token back into the profile bundle so a
// later `am use` restores the fresh token, not a stale one.
func persistClaudeToken(tool, name string, t *token) {
	entries := loadProfileEntries(tool, name)
	for i := range entries {
		e := &entries[i]
		if e.Artifact.Kind != "keychain" || e.Artifact.Service != "Claude Code-credentials" {
			continue
		}
		var c claudeCreds
		if json.Unmarshal(e.Data, &c) != nil {
			continue
		}
		c.ClaudeAiOauth.AccessToken = t.Access
		c.ClaudeAiOauth.RefreshToken = t.Refresh
		c.ClaudeAiOauth.ExpiresAt = t.ExpiresAt.UnixMilli()
		if b, err := json.Marshal(&c); err == nil {
			e.Data = b
		}
	}
	enc := encrypt(packEntries(entries))
	_ = writeFileAtomic(bundlePath(tool, name), enc, 0o600)
}

type refreshResp struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

func refreshClaudeToken(refresh string) (*token, error) {
	body, _ := json.Marshal(map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": refresh,
		"client_id":     claudeOAuthClientID,
	})
	req, _ := http.NewRequest("POST", claudeTokenURL, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var rr refreshResp
	if err := json.Unmarshal(raw, &rr); err != nil {
		return nil, fmt.Errorf("bad refresh response (%d): %s", resp.StatusCode, string(raw))
	}
	if rr.Error != "" || rr.AccessToken == "" {
		return nil, fmt.Errorf("refresh rejected: %s %s", rr.Error, rr.ErrorDesc)
	}
	newRefresh := rr.RefreshToken
	if newRefresh == "" {
		newRefresh = refresh
	}
	return &token{
		Access:    rr.AccessToken,
		Refresh:   newRefresh,
		ExpiresAt: time.Now().Add(time.Duration(rr.ExpiresIn) * time.Second),
	}, nil
}

// ---- small os helpers kept here to keep proxy.go focused ----

func lookPath(name string) (string, error) { return exec.LookPath(name) }

func execProcess(bin string, argv, env []string) {
	if err := syscall.Exec(bin, argv, env); err != nil {
		die("exec %s: %v", bin, err)
	}
}
