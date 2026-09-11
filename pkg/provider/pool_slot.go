package provider

import (
	"os"
	"strings"

	"amux-accounts/pkg/types"
)

// poolBrandMethod maps a ProviderConfig.Type to the brand + method segments
// used in identity IDs ("claude:web:ninhle").
var poolBrandMethod = map[string][2]string{
	"claude_web":  {"claude", "web"},
	"chatgpt_web": {"chatgpt", "web"},
	"gemini_web":  {"gemini", "web"},
	"gemini":      {"gemini", "api"},
}

// NamedPoolID returns the short identity ID for a provider type + email
// (e.g. claude_web + ninhle@x.com → "claude:web:ninhle"), or "" if the type
// has no brand/method mapping or the email has no local part.
func NamedPoolID(providerType, email string) string {
	bm, ok := poolBrandMethod[providerType]
	if !ok {
		return ""
	}
	return types.AccountNamedID(bm[0], bm[1], email)
}

// NamedPoolIDWithDomain returns the disambiguated identity ID when two emails
// share a local part: claude_web + ninhle@gmail.com → "claude:web:ninhle-gmailcom".
func NamedPoolIDWithDomain(providerType, email string) string {
	bm, ok := poolBrandMethod[providerType]
	if !ok {
		return ""
	}
	return types.AccountNamedIDWithDomain(bm[0], bm[1], email)
}

// PoolSlot is the result of resolving where a login should land in the pool.
type PoolSlot struct {
	ID       string
	Priority int
	Relogin  bool // true = updating an existing account entry
	// RenameFrom is set when promoting a legacy/anonymous numeric ID to a
	// named identity ID (e.g. claudeweb:01 → claude:web:ninhle).
	RenameFrom string
	Enabled    *bool // preserved from existing entry on relogin
}

// ResolvePoolSlot finds an existing pool entry for the same account identity,
// or allocates a new named/numeric ID. email may be empty (falls back to
// next numeric ID for the type's legacy prefix).
func ResolvePoolSlot(path, providerType, email string) PoolSlot {
	email = strings.TrimSpace(email)
	prefix := PoolIDPrefix(providerType)

	f, _ := LoadConfigFile(path)
	maxPriority := 0
	maxNum := 0
	var sameType []ProviderConfig
	if f != nil {
		for _, p := range f.Providers {
			if p.Type == providerType {
				sameType = append(sameType, p)
				if p.Priority > maxPriority {
					maxPriority = p.Priority
				}
			}
			if prefix != "" {
				if pre, num, ok := types.ParseID(p.ID); ok && pre == prefix {
					if num > maxNum {
						maxNum = num
					}
				}
			}
		}
	}

	wanted := chooseNamedPoolID(providerType, email, sameType)

	// Exact email match always wins (relogin keeps existing ID).
	for _, p := range sameType {
		if email != "" && p.Account != "" && strings.EqualFold(p.Account, email) {
			return PoolSlot{
				ID:       p.ID,
				Priority: p.Priority,
				Relogin:  true,
				Enabled:  p.Enabled,
			}
		}
	}

	// ID match only when the row has no account yet (legacy/anonymous).
	for _, p := range sameType {
		if wanted == "" || p.ID != wanted {
			continue
		}
		if strings.TrimSpace(p.Account) != "" && !strings.EqualFold(p.Account, email) {
			continue
		}
		return PoolSlot{
			ID:       p.ID,
			Priority: p.Priority,
			Relogin:  true,
			Enabled:  p.Enabled,
		}
	}

	// Promote a single anonymous (no Account) entry of this type to the
	// named identity — avoids leaving an orphan claudeweb:01 after first
	// identity-aware login.
	if wanted != "" && email != "" {
		var anon []ProviderConfig
		for _, p := range sameType {
			if strings.TrimSpace(p.Account) == "" {
				anon = append(anon, p)
			}
		}
		if len(anon) == 1 && !idTaken(f, wanted, anon[0].ID) {
			return PoolSlot{
				ID:         wanted,
				Priority:   anon[0].Priority,
				Relogin:    true,
				RenameFrom: anon[0].ID,
				Enabled:    anon[0].Enabled,
			}
		}
	}

	if wanted != "" && !idTaken(f, wanted, "") {
		prio := defaultPriorityForType(providerType)
		if len(sameType) > 0 {
			prio = maxPriority + 1
		}
		return PoolSlot{ID: wanted, Priority: prio, Relogin: false}
	}

	// Named ID taken (shouldn't happen after chooseNamedPoolID) → numeric fallback.
	if prefix == "" {
		prefix = strings.ReplaceAll(providerType, "_", "")
	}
	id := types.FormatID(prefix, maxNum+1)
	prio := defaultPriorityForType(providerType)
	if len(sameType) > 0 {
		prio = maxPriority + 1
	}
	return PoolSlot{ID: id, Priority: prio, Relogin: false}
}

// chooseNamedPoolID picks short local-part ID, or local-domain when another
// pool account already uses the same local part under a different email.
func chooseNamedPoolID(providerType, email string, sameType []ProviderConfig) string {
	simple := NamedPoolID(providerType, email)
	if simple == "" {
		return ""
	}
	local := types.EmailLocalPart(email)
	if local == "" {
		return simple
	}

	conflict := false
	for _, p := range sameType {
		if p.Account != "" && strings.EqualFold(p.Account, email) {
			return p.ID // already have this account — keep its ID
		}
		otherLocal := ""
		if p.Account != "" {
			otherLocal = types.EmailLocalPart(p.Account)
		}
		if otherLocal == local && !strings.EqualFold(p.Account, email) {
			conflict = true
			break
		}
		if p.ID == simple && (p.Account == "" || !strings.EqualFold(p.Account, email)) {
			conflict = true
			break
		}
	}
	if !conflict {
		return simple
	}
	disambig := NamedPoolIDWithDomain(providerType, email)
	if disambig == "" || disambig == simple {
		return simple
	}
	return disambig
}

func idTaken(f *AccountsFile, id, except string) bool {
	if f == nil || id == "" {
		return false
	}
	for _, p := range f.Providers {
		if p.ID == id && p.ID != except {
			return true
		}
	}
	return false
}

func defaultPriorityForType(providerType string) int {
	switch providerType {
	case "chatgpt_web":
		return PriorityWebChatGPT
	case "claude_web":
		return PriorityWebClaude
	case "gemini_web":
		return PriorityWebGemini
	case "gemini":
		return PriorityAPIGemini
	default:
		return PriorityAPICustom
	}
}

// UpsertPoolProvider writes p into the pool. If renameFrom is set, the
// existing row with that ID is replaced (ID change) instead of appending.
func UpsertPoolProvider(path string, p ProviderConfig, renameFrom string) error {
	if renameFrom == "" || renameFrom == p.ID {
		return AddOrUpdateProvider(path, p)
	}
	f, err := LoadConfigFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if f == nil {
		f = &AccountsFile{}
	}
	for i, existing := range f.Providers {
		if existing.ID == renameFrom {
			f.Providers[i] = p
			return SaveConfigFile(path, f)
		}
	}
	return AddOrUpdateProvider(path, p)
}
