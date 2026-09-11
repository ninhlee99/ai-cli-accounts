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
	Type     string `json:"type"` // "openai_compatible" | "chatgpt_web" | "claude_web" | "gemini"
	Priority int    `json:"priority"`
	Enabled  *bool  `json:"enabled,omitempty"`

	// Account is the signed-in email (or other stable identity). Used to
	// recognize re-logins of the same person so credentials update in place
	// under a stable ID like "claude:web:ninhle".
	Account string `json:"account,omitempty"`

	// openai_compatible & gemini
	BaseURL string `json:"baseUrl,omitempty"`
	APIKey  string `json:"apiKey,omitempty"`
	Model   string `json:"model,omitempty"`

	// chatgpt_web
	SessionToken string `json:"sessionToken,omitempty"`
	RefreshToken string `json:"refreshToken,omitempty"`

	// claude_web
	SessionKey string `json:"sessionKey,omitempty"`

	// gemini_web / optional raw Cookie header
	Cookies string `json:"cookies,omitempty"`

	// Claude web: reuse one chat_conversations UUID across am chat / proxy
	// restarts until rate-limit or 404 forces a new thread.
	OrgID          string `json:"orgId,omitempty"`
	ConversationID string `json:"conversationId,omitempty"`
	// ChatGPT web: last assistant message id (next turn's parent_message_id).
	ParentMessageID string `json:"parentMessageId,omitempty"`
	// Gemini web: JSON array of chat.metadata (cid/rid/rcid/…).
	MetadataJSON string `json:"metadataJson,omitempty"`
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
	case "gemini_web":
		return strings.TrimSpace(p.Cookies) != "" || ResolveSecret(p.SessionKey) != ""
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
	"gemini_web":  "geminiweb",
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

// SetEnabled turns a pool provider on or off. Disabled providers are skipped
// by the pool picker (same as Enabled:false in accounts.json).
func SetEnabled(path, id string, enabled bool) error {
	f, err := LoadConfigFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if f == nil {
		return fmt.Errorf("no provider with id %q in pool (see: am accounts)", id)
	}
	for i, p := range f.Providers {
		if p.ID == id {
			v := enabled
			f.Providers[i].Enabled = &v
			return SaveConfigFile(path, f)
		}
	}
	if prefix, _, ok := types.ParseID(id); ok && prefix == "codexcli" {
		v := enabled
		f.Providers = append(f.Providers, ProviderConfig{ID: id, Type: "codex_cli", Enabled: &v})
		return SaveConfigFile(path, f)
	}
	return fmt.Errorf("no provider with id %q in pool (see: am accounts)", id)
}

// SetModel updates the target model of one provider by ID and persists it.
func SetModel(path, id, model string) error {
	f, err := LoadConfigFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if f == nil {
		f = &AccountsFile{}
	}

	for i, p := range f.Providers {
		if p.ID == id {
			f.Providers[i].Model = model
			return SaveConfigFile(path, f)
		}
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
			model = "gemini-3.6-flash"
		}
		return NewGeminiAdapter(p.ID, p.Priority, ResolveSecret(p.APIKey), model), nil

	case "chatgpt_web":
		return &ChatGPTWebAdapter{
			AdapterID:       p.ID,
			PriorityLvl:     p.Priority,
			SessionToken:    ResolveSecret(p.SessionToken),
			TargetModel:     p.Model,
			convID:          p.ConversationID,
			parentMessageID: p.ParentMessageID,
		}, nil

	case "claude_web":
		return &ClaudeWebAdapter{
			AdapterID:   p.ID,
			PriorityLvl: p.Priority,
			SessionKey:  ResolveSecret(p.SessionKey),
			Cookies:     p.Cookies,
			TargetModel: p.Model,
			orgID:       p.OrgID,
			convUUID:    p.ConversationID,
		}, nil

	case "gemini_web":
		sk := ResolveSecret(p.SessionKey)
		cookies := p.Cookies
		if cookies == "" && sk != "" {
			cookies = "__Secure-1PSID=" + sk
		}
		return &GeminiWebAdapter{
			AdapterID:   p.ID,
			PriorityLvl: p.Priority,
			Cookies:     cookies,
			TargetModel: p.Model,
			cid:         p.ConversationID,
			metadataJSON: p.MetadataJSON,
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
					log.Printf("amux: skip provider %q (type=%q): %v", p.ID, p.Type, err)
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

// Pool priority bands (lower = tried first by AccountPoolRouter):
//
//	API keys / OpenAI-compatible  ……  1–19
//	Web sessions (ChatGPT/Claude/Codex)  20–49
const (
	PriorityAPIGitHub  = 1
	PriorityAPIGemini  = 2
	PriorityAPIGroq    = 3
	PriorityAPICustom  = 10
	PriorityWebChatGPT = 20
	PriorityWebGemini  = 22
	PriorityWebClaude  = 25
	PriorityWebCodex   = 30
)

// codexPoolAdapter builds the Codex CLI token-reuse adapter if a live
// ~/.codex/auth.json is present. providers is accounts.json's current
// provider list, consulted only for an optional "codex_cli"-typed override
// row (set via `am accounts priority <codexcli-id> <N>`, or disabled via
// Enabled:false) — see SetPriority.
func codexPoolAdapter(providers []ProviderConfig) types.ProviderAdapter {
	if !CodexAuthAvailable() {
		return nil
	}

	priority := PriorityWebCodex // web-session tier; after API keys
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

// UpdateProviderCookies merges a full Cookie header into an existing provider
// entry (used after CDP refresh so Cloudflare clearance stays current).
func UpdateProviderCookies(path, id, sessionKey, cookies string) error {
	f, err := LoadConfigFile(path)
	if err != nil {
		return err
	}
	for i, p := range f.Providers {
		if p.ID != id {
			continue
		}
		if sessionKey != "" {
			f.Providers[i].SessionKey = sessionKey
		}
		f.Providers[i].Cookies = cookies
		return SaveConfigFile(path, f)
	}
	return fmt.Errorf("provider %s not found", id)
}

// UpdateProviderConversation persists Claude web org+conversation IDs so the
// next process reuses the same thread (empty conv clears → next send creates).
func UpdateProviderConversation(path, id, orgID, conversationID string) error {
	return UpdateProviderChatState(path, id, ChatState{
		OrgID:          orgID,
		ConversationID: conversationID,
	})
}

// ChatState is the persisted multi-turn thread for web providers.
type ChatState struct {
	OrgID           string
	ConversationID  string
	ParentMessageID string
	MetadataJSON    string
	ClearParent     bool // when true, wipe ParentMessageID even if empty
	ClearMetadata   bool
}

// UpdateProviderChatState merges conversation continuity fields for web adapters.
func UpdateProviderChatState(path, id string, st ChatState) error {
	f, err := LoadConfigFile(path)
	if err != nil {
		return err
	}
	for i, p := range f.Providers {
		if p.ID != id {
			continue
		}
		f.Providers[i].OrgID = st.OrgID
		f.Providers[i].ConversationID = st.ConversationID
		if st.ParentMessageID != "" || st.ClearParent {
			f.Providers[i].ParentMessageID = st.ParentMessageID
		}
		if st.MetadataJSON != "" || st.ClearMetadata {
			f.Providers[i].MetadataJSON = st.MetadataJSON
		}
		return SaveConfigFile(path, f)
	}
	return fmt.Errorf("provider %s not found", id)
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
