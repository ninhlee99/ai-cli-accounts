package provider

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"amux-accounts/pkg/profile"
	"amux-accounts/pkg/types"
)

type ProviderConfig struct {
	ID       string `json:"id"`
	Type     string `json:"type"` // "openai_compatible" | "duckduckgo" | "chatgpt_web" | "claude_web" | "gemini"
	Priority int    `json:"priority"`
	Enabled  *bool  `json:"enabled,omitempty"`

	// openai_compatible & gemini
	BaseURL string `json:"baseUrl,omitempty"`
	APIKey  string `json:"apiKey,omitempty"`
	Model   string `json:"model,omitempty"`

	// chatgpt_web
	SessionToken string `json:"sessionToken,omitempty"`

	// claude_web
	SessionKey string `json:"sessionKey,omitempty"`

	// gemini_web
	Cookies string `json:"cookies,omitempty"`
}

// IsConfigured reports whether the provider has valid credentials / configuration.
func (p ProviderConfig) IsConfigured() bool {
	if p.Enabled != nil && !*p.Enabled {
		return false
	}
	switch p.Type {
	case "openai_compatible", "gemini":
		return ResolveSecret(p.APIKey) != ""
	case "chatgpt_web":
		return ResolveSecret(p.SessionToken) != ""
	case "claude_web":
		return ResolveSecret(p.SessionKey) != ""
	case "duckduckgo":
		if p.Enabled != nil {
			return *p.Enabled
		}
		return false
	}
	return false
}

type AccountsFile struct {
	Providers []ProviderConfig `json:"providers"`
}

func DefaultAccountsPath() string {
	return filepath.Join(types.BaseDir(), "accounts.json")
}

// legacyIDPrefix maps the old fixed literal IDs the built-in `am login`
// providers used to write (one account per provider, overwritten on every
// re-login) to their unified-ID prefix. Used once by MigrateLegacyIDs to
// rewrite accounts.json in place; anything not in this map (custom `am api
// add <name>` providers) is left untouched.
var legacyIDPrefix = map[string]string{
	"claude-web":       "claudeweb",
	"chatgpt-web":      "chatgptweb",
	"google-ai-studio": "geminiapi",
	"github-models":    "githubapi",
	"groq":             "groqapi",
}

// poolIDPrefix maps a ProviderConfig.Type to its unified-ID prefix, for the
// built-in provider types that always get one. Used by the loginXxx flows in
// pkg/ui/login.go to compute the next free ID when adding a session.
var poolIDPrefix = map[string]string{
	"claude_web":  "claudeweb",
	"chatgpt_web": "chatgptweb",
	"gemini":      "geminiapi",
}

// PoolIDPrefix returns the unified-ID prefix for a built-in pool provider
// type (e.g. "claude_web" -> "claudeweb"), or "" if the type has no fixed
// prefix (openai_compatible providers added via `am api add` keep whatever
// name the user chose).
func PoolIDPrefix(providerType string) string {
	return poolIDPrefix[providerType]
}

// MigrateLegacyIDs rewrites any provider in accounts.json still using one of
// the old fixed literal IDs (claude-web, chatgpt-web, google-ai-studio,
// github-models, groq) to the new "<prefix>:<NN>" format. Safe to call on
// every run: once IDs are migrated it's a no-op. Custom `am api add` entries
// (any ID not in legacyIDPrefix) are left untouched.
func MigrateLegacyIDs(path string) error {
	f, err := LoadConfigFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if f == nil || len(f.Providers) == 0 {
		return nil
	}

	// Count existing entries per prefix so a migrated ID doesn't collide
	// with one that (in theory) already exists under the new format.
	counts := map[string]int{}
	for _, p := range f.Providers {
		if prefix, _, ok := types.ParseID(p.ID); ok {
			counts[prefix]++
		}
	}

	changed := false
	for i, p := range f.Providers {
		prefix, isLegacy := legacyIDPrefix[p.ID]
		if !isLegacy {
			continue
		}
		counts[prefix]++
		f.Providers[i].ID = types.FormatID(prefix, counts[prefix])
		changed = true
	}

	if !changed {
		return nil
	}
	return SaveConfigFile(path, f)
}

// SetPriority updates the priority of one provider by ID and persists it. If
// id isn't found and looks like a "codex_cli"-shaped unified ID (the Codex
// CLI token-reuse adapter, which has no real accounts.json entry until a
// priority override is set for it), a bare placeholder row is inserted
// instead of erroring.
func SetPriority(path, id string, priority int) error {
	f, err := LoadConfigFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if f == nil {
		f = &AccountsFile{}
	}

	for i, p := range f.Providers {
		if p.ID == id {
			f.Providers[i].Priority = priority
			return SaveConfigFile(path, f)
		}
	}

	if prefix, _, ok := types.ParseID(id); ok && prefix == "codexcli" {
		f.Providers = append(f.Providers, ProviderConfig{ID: id, Type: "codex_cli", Priority: priority})
		return SaveConfigFile(path, f)
	}

	return fmt.Errorf("no provider with id %q in pool (see: am accounts)", id)
}

func ResolveSecret(val string) string {
	if strings.HasPrefix(val, "env:") {
		return os.Getenv(strings.TrimPrefix(val, "env:"))
	}
	return val
}

func BuildAdapter(p ProviderConfig) (types.ProviderAdapter, error) {
	switch p.Type {
	case "openai_compatible":
		if p.BaseURL == "" {
			return nil, fmt.Errorf("baseUrl is required")
		}
		model := p.Model
		if model == "" {
			model = "gpt-4o"
		}
		return &OpenAICompatibleAdapter{
			AdapterID:   p.ID,
			PriorityLvl: p.Priority,
			BaseURL:     p.BaseURL,
			APIKey:      ResolveSecret(p.APIKey),
			TargetModel: model,
		}, nil

	case "gemini":
		model := p.Model
		if model == "" {
			model = "gemini-2.0-flash"
		}
		return NewGeminiAdapter(p.ID, p.Priority, ResolveSecret(p.APIKey), model), nil

	case "duckduckgo":
		model := p.Model
		if model == "" {
			model = "claude-3-haiku-20240307"
		}
		return &DuckDuckGoAdapter{
			AdapterID:   p.ID,
			PriorityLvl: p.Priority,
			TargetModel: model,
		}, nil

	case "chatgpt_web":
		return &ChatGPTWebAdapter{
			AdapterID:    p.ID,
			PriorityLvl:  p.Priority,
			SessionToken: ResolveSecret(p.SessionToken),
			TargetModel:  p.Model,
		}, nil

	case "claude_web":
		return &ClaudeWebAdapter{
			AdapterID:   p.ID,
			PriorityLvl: p.Priority,
			SessionKey:  ResolveSecret(p.SessionKey),
			TargetModel: p.Model,
		}, nil

	default:
		return nil, fmt.Errorf("unsupported provider type: %s", p.Type)
	}
}

// LoadAccounts reads accounts from accounts.json, returning only configured/authenticated adapters.
func LoadAccounts(path string) ([]types.ProviderAdapter, error) {
	var adapters []types.ProviderAdapter

	// 1. Load accounts.json
	var providers []ProviderConfig
	b, err := os.ReadFile(path)
	if err == nil {
		var f AccountsFile
		if err := json.Unmarshal(b, &f); err == nil {
			providers = f.Providers
			for _, p := range f.Providers {
				if !p.IsConfigured() {
					continue
				}
				a, err := BuildAdapter(p)
				if err != nil {
					log.Printf("am: skip provider %q (type=%q): %v", p.ID, p.Type, err)
					continue
				}
				adapters = append(adapters, a)
			}
		}
	}

	// 2. Auto-surface the Codex CLI token-reuse adapter once `am add codex`
	// has snapshotted a live login — no separate `am login codex` step. It
	// has no secret-bearing accounts.json row of its own (see IsConfigured/
	// BuildAdapter, which have no "codex_cli" case and so never touch it
	// above); only an optional priority-override/opt-out row, consulted here.
	if a := codexPoolAdapter(providers); a != nil {
		adapters = append(adapters, a)
	}

	return adapters, nil
}

// codexPoolAdapter builds the Codex CLI token-reuse adapter if a live
// ~/.codex/auth.json is present. providers is accounts.json's current
// provider list, consulted only for an optional "codex_cli"-typed override
// row (set via `am accounts priority <codexcli-id> <N>`, or disabled via
// Enabled:false) — see SetPriority.
func codexPoolAdapter(providers []ProviderConfig) types.ProviderAdapter {
	if !CodexAuthAvailable() {
		return nil
	}

	priority := 6 // same default tier claude-web used to occupy
	for i := range providers {
		if providers[i].Type != "codex_cli" {
			continue
		}
		if providers[i].Enabled != nil && !*providers[i].Enabled {
			return nil // explicit opt-out
		}
		priority = providers[i].Priority
		break
	}

	return &CodexCLIAdapter{AdapterID: codexPoolID(), PriorityLvl: priority}
}

// CodexAutoRow returns a synthetic display row for the auto-surfaced Codex
// CLI token-reuse adapter, for callers like `am accounts` that want to show
// it even though (until a priority override is set via `am accounts
// priority`) it has no real entry in accounts.json. ok is false when no live
// Codex login is available, or an explicit opt-out (Enabled:false) row
// already exists in providers.
func CodexAutoRow(providers []ProviderConfig) (ProviderConfig, bool) {
	a, ok := codexPoolAdapter(providers).(*CodexCLIAdapter)
	if !ok || a == nil {
		return ProviderConfig{}, false
	}
	return ProviderConfig{ID: a.AdapterID, Type: "codex_cli", Priority: a.PriorityLvl}, true
}

// codexPoolID resolves the unified ID of the currently active codex
// profile, falling back to the first saved codex profile, then to
// "codexcli:01" if no profile has ever been saved (a bare `codex login`
// with no `am add codex` snapshot yet).
func codexPoolID() string {
	metas := profile.ListProfiles("codex")
	if active := profile.ReadActivePointer("codex"); active != "" {
		for _, m := range metas {
			if m.Name == active {
				return m.ID
			}
		}
	}
	if len(metas) > 0 {
		return metas[0].ID
	}
	return types.FormatID(profile.IDPrefixForTool("codex"), 1)
}

func LoadConfigFile(path string) (*AccountsFile, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return &AccountsFile{}, err
	}
	var f AccountsFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	return &f, nil
}

func SaveConfigFile(path string, f *AccountsFile) error {
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

func AddOrUpdateProvider(path string, p ProviderConfig) error {
	f, err := LoadConfigFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if f == nil {
		f = &AccountsFile{}
	}

	updated := false
	for i, existing := range f.Providers {
		if existing.ID == p.ID {
			f.Providers[i] = p
			updated = true
			break
		}
	}
	if !updated {
		f.Providers = append(f.Providers, p)
	}
	return SaveConfigFile(path, f)
}

func RemoveProvider(path string, id string) error {
	f, err := LoadConfigFile(path)
	if err != nil {
		return err
	}
	var kept []ProviderConfig
	for _, p := range f.Providers {
		if p.ID != id {
			kept = append(kept, p)
		}
	}
	f.Providers = kept
	return SaveConfigFile(path, f)
}
