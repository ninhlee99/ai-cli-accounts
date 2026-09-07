package main

import (
	"encoding/json"
	"os/exec"
	"syscall"
	"time"
)

// Claude Code stores its OAuth token as JSON under key "claudeAiOauth" inside
// the keychain item "Claude Code-credentials" (config is mirrored in
// ~/.claude.json). Claude Code owns refreshing this token — OAuth refresh
// tokens rotate on use, so the proxy must never race it. The proxy only reads.

const claudeKeychainService = "Claude Code-credentials"

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
	s, err := kcGet(claudeKeychainService, "")
	if err != nil {
		return nil
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

// installActiveProfile restores a profile's full credential set onto the system
// (keychain + files), so the live login matches the proxy's active account.
func installActiveProfile(name string) {
	for _, e := range loadProfileEntries("claude", name) {
		_ = applyEntry(e)
	}
}

// ---- small os helpers kept here to keep proxy.go focused ----

func lookPath(name string) (string, error) { return exec.LookPath(name) }

func execProcess(bin string, argv, env []string) {
	if err := syscall.Exec(bin, argv, env); err != nil {
		die("exec %s: %v", bin, err)
	}
}
