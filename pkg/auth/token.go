package auth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"ai-cli-accounts/pkg/types"
)

const (
	ClaudeKeychainService = "Claude Code-credentials"
	ClaudeOAuthClientID   = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	RefreshLead           = 2 * time.Minute
)

var ClaudeOAuthTokenURLs = []string{
	"https://platform.claude.com/v1/oauth/token",
	"https://console.anthropic.com/v1/oauth/token",
}

type ClaudeCreds struct {
	ClaudeAiOauth struct {
		AccessToken      string   `json:"accessToken"`
		RefreshToken     string   `json:"refreshToken"`
		ExpiresAt        int64    `json:"expiresAt"` // epoch millis
		Scopes           []string `json:"scopes"`
		SubscriptionType string   `json:"subscriptionType"`
	} `json:"claudeAiOauth"`
}

// ParseClaudeCreds unmarshals JSON credentials from Keychain or file.
func ParseClaudeCreds(b []byte) *types.Token {
	var c ClaudeCreds
	if json.Unmarshal(b, &c) != nil {
		return nil
	}
	o := c.ClaudeAiOauth
	if o.AccessToken == "" {
		return nil
	}
	return &types.Token{
		Access:    o.AccessToken,
		Refresh:   o.RefreshToken,
		ExpiresAt: time.UnixMilli(o.ExpiresAt),
		Remaining: -1,
	}
}

// LiveKeychainToken reads the OAuth token currently installed on the system.
func LiveKeychainToken() *types.Token {
	acct := types.CurrentUser()
	s, err := KCGet(ClaudeKeychainService, acct)
	if err != nil {
		s, err = KCGet(ClaudeKeychainService, "")
		if err != nil {
			return nil
		}
	}
	return ParseClaudeCreds([]byte(s))
}

// TokenExpiryNeedsRefresh reports whether an access token is expired or close to expiry.
func TokenExpiryNeedsRefresh(expAtMillis int64) bool {
	return time.Now().Add(RefreshLead).After(time.UnixMilli(expAtMillis))
}

type OAuthRefreshResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

// RefreshClaudeToken exchanges a refresh token for a fresh access token.
func RefreshClaudeToken(refreshToken string) (*OAuthRefreshResponse, error) {
	if refreshToken == "" {
		return nil, fmt.Errorf("no refresh token")
	}
	body, _ := json.Marshal(map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": refreshToken,
		"client_id":     ClaudeOAuthClientID,
	})
	client := &http.Client{Timeout: 10 * time.Second}

	var lastErr error
	for _, url := range ClaudeOAuthTokenURLs {
		out, err := postRefreshRequest(client, url, body)
		if err != nil {
			lastErr = err
			continue
		}
		return out, nil
	}
	return nil, fmt.Errorf("refresh token: %w", lastErr)
}

func postRefreshRequest(client *http.Client, url string, body []byte) (*OAuthRefreshResponse, error) {
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: status %d", url, resp.StatusCode)
	}
	var out OAuthRefreshResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("%s: decode: %w", url, err)
	}
	if out.AccessToken == "" {
		return nil, fmt.Errorf("%s: empty access_token in response", url)
	}
	return &out, nil
}

// RefreshedCredsJSON takes a keychain item's original JSON and replaces claudeAiOauth.
func RefreshedCredsJSON(original []byte, rr *OAuthRefreshResponse, oldRefresh string) ([]byte, int64, error) {
	var doc map[string]any
	if err := json.Unmarshal(original, &doc); err != nil {
		return nil, 0, fmt.Errorf("parse original creds: %w", err)
	}
	var old ClaudeCreds
	_ = json.Unmarshal(original, &old)

	expiresAt := time.Now().Add(time.Duration(rr.ExpiresIn) * time.Second).UnixMilli()
	newRefresh := rr.RefreshToken
	if newRefresh == "" {
		newRefresh = oldRefresh
	}

	claudeAiOauth := map[string]any{
		"accessToken":      rr.AccessToken,
		"refreshToken":     newRefresh,
		"expiresAt":        expiresAt,
		"scopes":           old.ClaudeAiOauth.Scopes,
		"subscriptionType": old.ClaudeAiOauth.SubscriptionType,
	}
	doc["claudeAiOauth"] = claudeAiOauth

	out, err := json.Marshal(doc)
	if err != nil {
		return nil, 0, fmt.Errorf("marshal refreshed creds: %w", err)
	}
	return out, expiresAt, nil
}

// RefreshLiveClaudeToken attempts to refresh the token currently in Keychain.
func RefreshLiveClaudeToken() (string, error) {
	live := LiveKeychainToken()
	if live == nil {
		return "", fmt.Errorf("no live keychain token")
	}
	if live.Refresh == "" {
		return "", fmt.Errorf("no refresh token in live keychain")
	}
	rr, err := RefreshClaudeToken(live.Refresh)
	if err != nil {
		return "", err
	}
	raw, err := KCGet(ClaudeKeychainService, KCAccount(ClaudeKeychainService))
	if err != nil {
		return "", err
	}
	newData, _, err := RefreshedCredsJSON([]byte(raw), rr, live.Refresh)
	if err != nil {
		return "", err
	}
	if err := KCSet(ClaudeKeychainService, KCAccount(ClaudeKeychainService), string(newData)); err != nil {
		return "", err
	}
	return rr.AccessToken, nil
}
