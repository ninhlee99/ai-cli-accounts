package provider

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"amux-accounts/pkg/auth"
	"amux-accounts/pkg/profile"
	"amux-accounts/pkg/types"
)

// DuplicateAPIKeyError means the same credential (API key / session token /
// session key) is already bound to another provider — one secret → one entry.
type DuplicateAPIKeyError struct {
	ExistingID string
}

func (e *DuplicateAPIKeyError) Error() string {
	return fmt.Sprintf("credential already used by provider %q — skipped", e.ExistingID)
}

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

// IsConfigured reports whether the provider has valid credentials and is in
// the rotate pool (Enabled != false).
func (p ProviderConfig) IsConfigured() bool {
	if !p.InRotatePool() {
		return false
	}
	return p.HasCredentials()
}

// InRotatePool is true unless Enabled is explicitly false.
func (p ProviderConfig) InRotatePool() bool {
	return p.Enabled == nil || *p.Enabled
}

// HasCredentials reports whether secrets/config look usable (ignores Enabled).
func (p ProviderConfig) HasCredentials() bool {
	switch p.Type {
	case "openai_compatible", "gemini":
		return ResolveSecret(p.APIKey) != ""
	case "chatgpt_web":
		return ResolveSecret(p.SessionToken) != ""
	case "claude_web":
		return ResolveSecret(p.SessionKey) != ""
	case "gemini_web":
		return strings.TrimSpace(p.Cookies) != "" || ResolveSecret(p.SessionKey) != ""
	case "codex_cli":
		return CodexAuthAvailable()
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
	"claude-web":       "claude:web",
	"chatgpt-web":      "chatgpt",
	"google-ai-studio": "gemini:api",
	"github-models":    "github:api",
	"groq":             "groq:api",
}

// poolIDPrefix maps a ProviderConfig.Type to its unified-ID prefix, for the
// built-in provider types that always get one. Used by the loginXxx flows in
// pkg/ui/login.go to compute the next free ID when adding a session.
var poolIDPrefix = map[string]string{
	"claude_web":  "claude:web",
	"chatgpt_web": "chatgpt",
	"gemini":      "gemini:api",
	"gemini_web":  "gemini:web",
}

// PoolIDPrefix returns the unified-ID prefix for a built-in pool provider
// type (e.g. "claude_web" -> "claude:web"), or "" if the type has no fixed
// prefix (openai_compatible providers added via `am api add` keep whatever
// name the user chose, except known hosts like OpenRouter which get
// "openrouter:api").
func PoolIDPrefix(providerType string) string {
	return poolIDPrefix[providerType]
}

// NextIDForPrefix returns FormatID(prefix, maxN+1) based on existing providers
// whose ParseID prefix matches. Used when adding another OpenRouter (etc.) key.
func NextIDForPrefix(path, prefix string) (string, error) {
	f, err := LoadConfigFile(path)
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	maxN := 0
	if f != nil {
		for _, p := range f.Providers {
			pre, n, ok := types.ParseID(p.ID)
			if ok && pre == prefix && n > maxN {
				maxN = n
			}
			// Treat unnumbered openrouter:api as slot 1 for collision avoidance.
			if prefix == "openrouter:api" && (p.ID == "openrouter" || p.ID == "openrouter:api") && maxN < 1 {
				maxN = 1
			}
		}
	}
	return types.FormatID(prefix, maxN+1), nil
}

// IsOpenRouterEndpoint reports whether baseURL points at OpenRouter.
func IsOpenRouterEndpoint(baseURL string) bool {
	u := strings.ToLower(baseURL)
	return strings.Contains(u, "openrouter.ai")
}

// MigrateLegacyIDs rewrites any provider in accounts.json still using one of
// the old fixed literal IDs (claude-web, chatgpt-web, google-ai-studio,
// github-models, groq) or compact flat IDs (geminiapi:01, claudeweb:01, …)
// to the brand[:method]:NN format. Also remaps account/msg fields in
// usage.log, requests.log, and events.log. Safe to call on every run: once
// migrated it's a no-op. Custom `am api add` entries stay untouched except
// bare "openrouter" / "openrouter:api" → "openrouter:api:01".
func MigrateLegacyIDs(path string) error {
	f, err := LoadConfigFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			remapAccountLogs()
			return nil
		}
		return err
	}
	if f == nil || len(f.Providers) == 0 {
		remapAccountLogs()
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

	// Compact → brand:method (geminiapi:01 → gemini:api:01).
	used := map[string]bool{}
	for _, p := range f.Providers {
		used[p.ID] = true
	}
	for i, p := range f.Providers {
		newID, ok := types.MigrateCompactID(p.ID)
		if !ok || newID == p.ID || used[newID] {
			continue
		}
		delete(used, p.ID)
		used[newID] = true
		f.Providers[i].ID = newID
		changed = true
	}

	if changed {
		if err := SaveConfigFile(path, f); err != nil {
			return err
		}
	}
	remapAccountLogs()
	return nil
}

func remapAccountLogs() {
	base := types.BaseDir()
	_ = remapJSONLAccountFields(filepath.Join(base, "usage.log"))
	_ = remapJSONLAccountFields(filepath.Join(base, "requests.log"))
	_ = remapJSONLAccountFields(filepath.Join(base, "events.log"))
}

// remapJSONLAccountFields rewrites "account" values (and free-text "msg") in a
// JSONL file from compact IDs (geminiapi:01) to brand:method form.
func remapJSONLAccountFields(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if len(data) == 0 {
		return nil
	}
	lines := strings.Split(string(data), "\n")
	changed := false
	for i, line := range lines {
		trim := strings.TrimSpace(line)
		if trim == "" {
			continue
		}
		var m map[string]any
		if json.Unmarshal([]byte(trim), &m) != nil {
			continue
		}
		lineChanged := false
		if acct, ok := m["account"].(string); ok {
			if newID, ok2 := types.MigrateCompactID(acct); ok2 && newID != acct {
				m["account"] = newID
				lineChanged = true
			}
		}
		if msg, ok := m["msg"].(string); ok {
			if newMsg := types.RemapAccountIDsInText(msg); newMsg != msg {
				m["msg"] = newMsg
				lineChanged = true
			}
		}
		if !lineChanged {
			continue
		}
		b, err := json.Marshal(m)
		if err != nil {
			continue
		}
		lines[i] = string(b)
		changed = true
	}
	if !changed {
		return nil
	}
	out := strings.Join(lines, "\n")
	return os.WriteFile(path, []byte(out), 0o600)
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

	if prefix, _, ok := types.ParseID(id); ok && (prefix == "codex" || prefix == "codexcli") {
		f.Providers = append(f.Providers, ProviderConfig{ID: id, Type: "codex_cli", Priority: priority})
		return SaveConfigFile(path, f)
	}

	return fmt.Errorf("no provider with id %q in pool (see: am accounts)", id)
}

// SetEnabled turns a pool provider on or off. Disabled (Enabled:false) means
// removed from rotate/failover pool; the account stays in accounts.json and
// can still be selected via X-Provider / X-Model on API requests.
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
	if prefix, _, ok := types.ParseID(id); ok && (prefix == "codex" || prefix == "codexcli") {
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

// LoadAccounts reads accounts from accounts.json, returning only rotate-pool
// adapters (Enabled != false + credentials).
func LoadAccounts(path string) ([]types.ProviderAdapter, error) {
	return loadAccounts(path, true)
}

// LoadAllAddressable returns every adapter with credentials that is not
// turned off (Enabled != false) — including ones outside the priority
// rotate order, so X-Provider can still pin to a specific enabled account.
// A disabled ("am off") provider is never returned here: off must mean off
// for every path (auto-rotate, X-Provider, force-switch), not just rotation.
func LoadAllAddressable(path string) ([]types.ProviderAdapter, error) {
	return loadAccounts(path, false)
}

// LookupAdapter builds one adapter by ID, even when out of the priority
// rotate order — but never for a disabled ("am off") provider.
func LookupAdapter(path, id string) (types.ProviderAdapter, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("empty provider id")
	}
	all, err := LoadAllAddressable(path)
	if err != nil {
		return nil, err
	}
	for _, a := range all {
		if a.ID() == id {
			return a, nil
		}
	}
	return nil, fmt.Errorf("provider %q not found or missing credentials", id)
}

func loadAccounts(path string, rotateOnly bool) ([]types.ProviderAdapter, error) {
	var adapters []types.ProviderAdapter

	var providers []ProviderConfig
	b, err := readAccountsFileBytes(path)
	if err == nil {
		var f AccountsFile
		if err := json.Unmarshal(b, &f); err == nil {
			providers = f.Providers
			for _, p := range f.Providers {
				// A disabled ("am off") provider is never addressable, in
				// or out of the rotate pool — X-Provider must not be a way
				// to reach an account the user turned off.
				if !p.InRotatePool() {
					continue
				}
				if rotateOnly {
					if !p.IsConfigured() {
						continue
					}
				} else {
					if !p.HasCredentials() {
						continue
					}
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

	if a := codexPoolAdapter(providers); a != nil {
		if rotateOnly {
			adapters = append(adapters, a)
		} else {
			// Always include codex when credentials exist unless explicit opt-out
			// and rotateOnly — for addressable, include even if disabled? If
			// Enabled:false, codexPoolAdapter returns nil. Build manually:
			adapters = append(adapters, a)
		}
	} else if !rotateOnly && CodexAuthAvailable() {
		// Out-of-pool codex still addressable via X-Provider.
		for i := range providers {
			if providers[i].Type == "codex_cli" && providers[i].Enabled != nil && !*providers[i].Enabled {
				adapters = append(adapters, &CodexCLIAdapter{AdapterID: codexPoolID(), PriorityLvl: providers[i].Priority})
				break
			}
		}
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
// "codex:01" if no profile has ever been saved (a bare `codex login`
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
	b, err := readAccountsFileBytes(path)
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
	return writeAccountsFileBytes(path, append(b, '\n'))
}

// accountsFileMagic tags an encrypted-at-rest accounts.json so it can be
// told apart from the legacy plaintext format (which always starts with
// '{'). Credentials in this file (API keys, session tokens, cookies) sit on
// disk for as long as the account is configured, unlike the .amp export
// bundle (already AES-GCM sealed via pkg/auth) — so it gets the same
// protection instead of relying on file mode 0600 alone.
var accountsFileMagic = []byte("AMENC1:")

// readAccountsFileBytes returns the plaintext JSON for path, transparently
// decrypting if the file was written by writeAccountsFileBytes. A file that
// predates this encryption (starts with plaintext JSON) is returned as-is —
// it gets encrypted on the next SaveConfigFile.
func readAccountsFileBytes(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if !bytes.HasPrefix(b, accountsFileMagic) {
		return b, nil
	}
	enc := b[len(accountsFileMagic):]
	dec, err := base64.StdEncoding.DecodeString(string(bytes.TrimSpace(enc)))
	if err != nil {
		return nil, fmt.Errorf("decode accounts file: %w", err)
	}
	plain, err := auth.Decrypt(dec)
	if err != nil {
		return nil, fmt.Errorf("decrypt accounts file: %w", err)
	}
	return plain, nil
}

// writeAccountsFileBytes encrypts plain (a full accounts.json document) with
// the machine's master key before writing it to path.
func writeAccountsFileBytes(path string, plain []byte) error {
	enc, err := auth.Encrypt(plain)
	if err != nil {
		return fmt.Errorf("encrypt accounts file: %w", err)
	}
	out := append(append([]byte{}, accountsFileMagic...), []byte(base64.StdEncoding.EncodeToString(enc))...)
	return os.WriteFile(path, out, 0o600)
}

// sameSecret reports whether two stored credential fields refer to the same
// secret (resolved env: refs, or identical raw strings including env:FOO).
func sameSecret(a, b string) bool {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	ra, rb := strings.TrimSpace(ResolveSecret(a)), strings.TrimSpace(ResolveSecret(b))
	return ra != "" && rb != "" && ra == rb
}

func providerSecrets(p ProviderConfig) []string {
	var out []string
	for _, s := range []string{p.APIKey, p.SessionToken, p.SessionKey} {
		if strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}

// findDuplicateCredentialID returns another provider's ID that already owns
// any of p's secrets (api key / session token / session key).
func findDuplicateCredentialID(f *AccountsFile, p ProviderConfig, skipIDs ...string) string {
	if f == nil {
		return ""
	}
	secrets := providerSecrets(p)
	if len(secrets) == 0 {
		return ""
	}
	skip := map[string]bool{}
	for _, id := range skipIDs {
		if id != "" {
			skip[id] = true
		}
	}
	for _, existing := range f.Providers {
		if skip[existing.ID] {
			continue
		}
		for _, es := range providerSecrets(existing) {
			for _, ps := range secrets {
				if sameSecret(es, ps) {
					return existing.ID
				}
			}
		}
	}
	return ""
}

// DeduplicateProvidersByCredential drops later entries that reuse an earlier
// provider's API key / session token / session key. Keeps first occurrence.
func DeduplicateProvidersByCredential(path string) (removed []string, err error) {
	f, err := LoadConfigFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if f == nil || len(f.Providers) == 0 {
		return nil, nil
	}
	var kept []ProviderConfig
	seen := make([]ProviderConfig, 0, len(f.Providers))
	for _, p := range f.Providers {
		dupOf := ""
		tmp := &AccountsFile{Providers: seen}
		if id := findDuplicateCredentialID(tmp, p); id != "" {
			dupOf = id
		}
		if dupOf != "" {
			removed = append(removed, p.ID)
			continue
		}
		seen = append(seen, p)
		kept = append(kept, p)
	}
	if len(removed) == 0 {
		return nil, nil
	}
	f.Providers = kept
	return removed, SaveConfigFile(path, f)
}

func AddOrUpdateProvider(path string, p ProviderConfig) error {
	f, err := LoadConfigFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if f == nil {
		f = &AccountsFile{}
	}

	if dup := findDuplicateCredentialID(f, p, p.ID); dup != "" {
		return &DuplicateAPIKeyError{ExistingID: dup}
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
