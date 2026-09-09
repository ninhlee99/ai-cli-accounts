package types

import (
	"os"
	"os/user"
	"path/filepath"
)

// BaseDir returns the root storage directory (~/.am, or overridden via
// $AM_HOME — the original var name — or $AM_DIR, checked second so either
// still works).
func BaseDir() string {
	if d := os.Getenv("AM_HOME"); d != "" {
		return d
	}
	if d := os.Getenv("AM_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".am")
}

// CurrentUser returns the current OS username.
func CurrentUser() string {
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return os.Getenv("USER")
}

// ToolConfig describes all configured tools and their artifacts.
type ToolConfig struct {
	Tools map[string]ToolSpec `json:"tools"`
}

// DefaultToolConfig returns the built-in tool specifications for Claude, Codex, and Gemini.
func DefaultToolConfig() ToolConfig {
	home, _ := os.UserHomeDir()
	j := func(p string) string { return filepath.Join(home, p) }
	return ToolConfig{Tools: map[string]ToolSpec{
		"claude": {Name: "claude", Artifacts: []Artifact{
			{Kind: "keychain", Service: "Claude Code-credentials", Account: CurrentUser()},
			{Kind: "file", Path: j(".claude.json"), AccountField: "oauthAccount.emailAddress"},
			{Kind: "file", Path: j(".claude/.credentials.json"), Optional: true},
		}},
		"codex": {Name: "codex", Artifacts: []Artifact{
			{Kind: "file", Path: j(".codex/auth.json"), AccountField: "jwt:tokens.id_token:email"},
		}},
		"gemini": {Name: "gemini", Artifacts: []Artifact{
			{Kind: "file", Path: j(".gemini/oauth_creds.json"), Optional: true, AccountField: "jwt:id_token:email"},
			{Kind: "file", Path: j(".gemini/google_accounts.json"), Optional: true, AccountField: "active"},
			{Kind: "file", Path: j(".gemini/installation_id"), Optional: true},
		}},
		// antigravity is a placeholder: no login-detection artifacts yet, so
		// it always shows up as "not logged in" / "(none — am add antigravity)"
		// until real detection logic exists for it.
		"antigravity": {Name: "antigravity", Artifacts: []Artifact{}},
	}}
}
