package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"
)

// Claude Code stores its OAuth token as JSON under key "claudeAiOauth" inside
// the keychain item "Claude Code-credentials" (config is mirrored in
// ~/.claude.json). Claude Code normally owns refreshing this token — OAuth
// refresh tokens rotate on use, so a running `claude` and `am` racing to
// refresh the same token would strand one of them. To stay out of that race
// while still handing back a token that works, `am` only refreshes a
// profile's token at the moment it activates it (switch/rotate), never a
// token that is currently live/in-use, and immediately persists whatever the
// refresh returned (both keychain and bundle) so the rotation is captured.

const claudeKeychainService = "Claude Code-credentials"

// Same OAuth client Claude Code itself uses. Public flow, no secret involved.
const claudeOAuthClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"

// Token endpoint moved from console.anthropic.com to platform.claude.com;
// try the new host first, fall back to the old one.
var claudeOAuthTokenURLs = []string{
	"https://platform.claude.com/v1/oauth/token",
	"https://console.anthropic.com/v1/oauth/token",
}

// refreshLead: refresh a token this long before it actually expires, so a
// request made right after switching doesn't race the expiry.
const refreshLead = 2 * time.Minute

type claudeCreds struct {
	ClaudeAiOauth struct {
		AccessToken      string   `json:"accessToken"`
		RefreshToken     string   `json:"refreshToken"`
		ExpiresAt        int64    `json:"expiresAt"` // epoch millis
		Scopes           []string `json:"scopes"`
		SubscriptionType string   `json:"subscriptionType"`
	} `json:"claudeAiOauth"`
}

// loadClaudeToken reads the token embedded in a saved profile bundle (the
// fallback used for accounts that aren't the one currently installed).
func loadClaudeToken(tool, name string) *token {
	for _, e := range loadProfileEntries(tool, name) {
		if e.Artifact.Kind != "keychain" || e.Artifact.Service != claudeKeychainService {
			continue
		}
		if t := parseClaudeCreds(e.Data); t != nil {
			return t
		}
	}
	return &token{remaining: -1}
}

// liveKeychainToken reads the OAuth token currently installed on the system.
func liveKeychainToken() *token {
	acct := currentUser()
	s, err := kcGet(claudeKeychainService, acct)
	if err != nil {
		s, err = kcGet(claudeKeychainService, "")
		if err != nil {
			return nil
		}
	}
	return parseClaudeCreds([]byte(s))
}

func parseClaudeCreds(b []byte) *token {
	var c claudeCreds
	if json.Unmarshal(b, &c) != nil {
		return nil
	}
	o := c.ClaudeAiOauth
	if o.AccessToken == "" {
		return nil
	}
	return &token{
		Access:    o.AccessToken,
		Refresh:   o.RefreshToken,
		ExpiresAt: time.UnixMilli(o.ExpiresAt),
		account:   liveAccountEmail(),
		remaining: -1,
	}
}

// liveAccountEmail reads the logged-in email from ~/.claude.json.
func liveAccountEmail() string {
	return detectAccount(toolSpec("claude"))
}

// tokenExpiryNeedsRefresh reports whether an access token expiring at expAt
// (epoch millis) is already expired or close enough to expiry to refresh now.
func tokenExpiryNeedsRefresh(expAtMillis int64) bool {
	return time.Now().Add(refreshLead).After(time.UnixMilli(expAtMillis))
}

type oauthRefreshResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

// refreshClaudeToken exchanges a refresh token for a fresh access token,
// trying each known token endpoint in turn. Returns the new access/refresh
// tokens and absolute expiry (epoch millis).
func refreshClaudeToken(refreshToken string) (*oauthRefreshResponse, error) {
	if refreshToken == "" {
		return nil, fmt.Errorf("no refresh token")
	}
	body, _ := json.Marshal(map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": refreshToken,
		"client_id":     claudeOAuthClientID,
	})
	client := &http.Client{Timeout: 10 * time.Second}

	var lastErr error
	for _, url := range claudeOAuthTokenURLs {
		out, err := postRefreshRequest(client, url, body)
		if err != nil {
			lastErr = err
			continue
		}
		return out, nil
	}
	return nil, fmt.Errorf("refresh token: %w", lastErr)
}

func postRefreshRequest(client *http.Client, url string, body []byte) (*oauthRefreshResponse, error) {
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
	var out oauthRefreshResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("%s: decode: %w", url, err)
	}
	if out.AccessToken == "" {
		return nil, fmt.Errorf("%s: empty access_token in response", url)
	}
	return &out, nil
}

// refreshedCredsJSON takes a keychain item's original JSON (which may carry
// sibling keys like "mcpOAuth" alongside "claudeAiOauth") and returns it with
// only the "claudeAiOauth" object replaced by the refreshed token — every
// other key is preserved untouched.
func refreshedCredsJSON(original []byte, rr *oauthRefreshResponse, oldRefresh string) ([]byte, int64, error) {
	var doc map[string]any
	if err := json.Unmarshal(original, &doc); err != nil {
		return nil, 0, fmt.Errorf("parse original creds: %w", err)
	}
	var old claudeCreds
	_ = json.Unmarshal(original, &old)

	expiresAt := time.Now().Add(time.Duration(rr.ExpiresIn) * time.Second).UnixMilli()
	newRefresh := rr.RefreshToken
	if newRefresh == "" {
		newRefresh = oldRefresh // some responses omit it when it didn't rotate
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

// installActiveProfile restores a profile's full credential set onto the
// system (keychain + files), so the live login matches the proxy's active
// account. If the profile's Claude Code token is expired (or close to it), it
// is refreshed first — otherwise Claude Code would see a dead access token
// with a burned (already-rotated) refresh token and force a re-login.
//
// Returns false only when a refresh was actually attempted and the exchange
// itself failed (revoked/expired refresh token) — the caller uses that to
// blacklist the profile from rotation until it's logged into again. A
// profile that simply didn't need refreshing (or never had a refresh token)
// still returns true; that's not a dead-refresh signal.
func installActiveProfile(name string) bool {
	entries := loadProfileEntries("claude", name)
	ok := true
	for i, e := range entries {
		if e.Artifact.Kind != "keychain" || e.Artifact.Service != claudeKeychainService {
			continue
		}
		var c claudeCreds
		if json.Unmarshal(e.Data, &c) != nil {
			break
		}
		if tokenExpiryNeedsRefresh(c.ClaudeAiOauth.ExpiresAt) {
			if c.ClaudeAiOauth.RefreshToken == "" {
				log.Printf("am: token for claude/%s is expired and has no refresh token", name)
				ok = false
				break
			}
			rr, err := refreshClaudeToken(c.ClaudeAiOauth.RefreshToken)
			if err != nil {
				log.Printf("am: refresh token for claude/%s failed: %v", name, err)
				ok = false
				break
			}
			newData, _, err := refreshedCredsJSON(e.Data, rr, c.ClaudeAiOauth.RefreshToken)
			if err != nil {
				log.Printf("am: rebuild refreshed creds for claude/%s failed: %v", name, err)
				ok = false
				break
			}
			entries[i].Data = newData
			if err := updateProfileEntry("claude", name, entries[i]); err != nil {
				log.Printf("am: could not persist refreshed token into bundle claude/%s: %v", name, err)
			}
		}
		break
	}
	if !ok {
		return false
	}
	for _, e := range entries {
		_ = applyEntry(e)
	}
	return true
}

// refreshLiveClaudeToken attempts to refresh the token currently installed in the Keychain.
func refreshLiveClaudeToken() (string, error) {
	live := liveKeychainToken()
	if live == nil {
		return "", fmt.Errorf("no live keychain token")
	}
	if live.Refresh == "" {
		return "", fmt.Errorf("no refresh token in live keychain")
	}
	rr, err := refreshClaudeToken(live.Refresh)
	if err != nil {
		return "", err
	}
	raw, err := kcGet(claudeKeychainService, kcAccount(claudeKeychainService))
	if err != nil {
		return "", err
	}
	newData, _, err := refreshedCredsJSON([]byte(raw), rr, live.Refresh)
	if err != nil {
		return "", err
	}
	if err := kcSet(claudeKeychainService, kcAccount(claudeKeychainService), string(newData)); err != nil {
		return "", err
	}
	return rr.AccessToken, nil
}
