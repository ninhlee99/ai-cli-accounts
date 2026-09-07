package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Artifact is one file or keychain entry that belongs to a tool's login state.
type Artifact struct {
	// Kind is "file" or "keychain".
	Kind string `json:"kind"`
	// Path is the absolute path for Kind=="file" (may contain ~).
	Path string `json:"path,omitempty"`
	// Service / Account identify a macOS generic-password item for Kind=="keychain".
	Service string `json:"service,omitempty"`
	Account string `json:"account,omitempty"`
	// Optional means a missing artifact is not an error when saving.
	Optional bool `json:"optional,omitempty"`
	// AccountField is a dotted path into a JSON file whose value names the
	// logged-in account (used by `am now`). Only meaningful for files.
	AccountField string `json:"accountField,omitempty"`
}

// ToolSpec describes how to snapshot / restore one CLI's auth state.
type ToolSpec struct {
	Name      string     `json:"name"`
	Artifacts []Artifact `json:"artifacts"`
}

type Config struct {
	Tools map[string]ToolSpec `json:"tools"`
}

func defaultConfig() Config {
	home, _ := os.UserHomeDir()
	j := func(p string) string { return filepath.Join(home, p) }
	return Config{Tools: map[string]ToolSpec{
		"claude": {Name: "claude", Artifacts: []Artifact{
			{Kind: "keychain", Service: "Claude Code-credentials", Account: currentUser()},
			{Kind: "file", Path: j(".claude.json"), AccountField: "oauthAccount.emailAddress"},
			{Kind: "file", Path: j(".claude/.credentials.json"), Optional: true},
		}},
		"codex": {Name: "codex", Artifacts: []Artifact{
			{Kind: "file", Path: j(".codex/auth.json"), AccountField: "tokens.account_id"},
		}},
		"gemini": {Name: "gemini", Artifacts: []Artifact{
			{Kind: "file", Path: j(".gemini/oauth_creds.json"), Optional: true},
			{Kind: "file", Path: j(".gemini/google_accounts.json"), Optional: true, AccountField: "active"},
			{Kind: "file", Path: j(".gemini/installation_id"), Optional: true},
		}},
	}}
}

func configPath() string { return filepath.Join(baseDir(), "config.json") }

func loadConfig() Config {
	b, err := os.ReadFile(configPath())
	if err != nil {
		c := defaultConfig()
		saveConfig(c)
		return c
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		die("bad config.json: %v", err)
	}
	// Merge in any tools added to defaults since the file was written.
	def := defaultConfig()
	for k, v := range def.Tools {
		if _, ok := c.Tools[k]; !ok {
			c.Tools[k] = v
		}
	}
	return c
}

func saveConfig(c Config) {
	if err := os.MkdirAll(baseDir(), 0o700); err != nil {
		die("mkdir: %v", err)
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	if err := os.WriteFile(configPath(), b, 0o600); err != nil {
		die("write config: %v", err)
	}
}
