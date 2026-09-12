package types

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// idPattern matches unified IDs:
//   brand:NN           → chatgpt:01, codex:01, openrouter:01
//   brand:method:NN    → claude:web:01, gemini:api:01, gemini:web:01
// Number is always zero-padded to at least 2 digits.
var idPattern = regexp.MustCompile(`^([a-z0-9]+(?::[a-z0-9]+)?):([0-9]{2,})$`)

// FormatID renders a prefix and a 1-based index into the unified ID format,
// zero-padding the number to at least 2 digits.
// Examples: FormatID("gemini:api", 1) → "gemini:api:01"
//
//	FormatID("chatgpt", 1) → "chatgpt:01"
func FormatID(prefix string, n int) string {
	return fmt.Sprintf("%s:%02d", prefix, n)
}

// ParseID splits a unified-format ID back into its prefix and number. ok is
// false if id doesn't match (legacy literals, custom api-add names, named
// identity IDs like claude:web:ninhle).
func ParseID(id string) (prefix string, n int, ok bool) {
	m := idPattern.FindStringSubmatch(id)
	if m == nil {
		return "", 0, false
	}
	num, err := strconv.Atoi(m[2])
	if err != nil {
		return "", 0, false
	}
	return m[1], num, true
}

// CompactPrefixMigrate maps old flat prefixes → new brand[:method] prefixes.
var CompactPrefixMigrate = map[string]string{
	"claudeweb":  "claude:web",
	"chatgptweb": "chatgpt",
	"geminiapi":  "gemini:api",
	"geminiweb":  "gemini:web",
	"codexcli":   "codex",
	"claudecli":  "claude:code",
	"geminicli":  "antigravity",
	"githubapi":  "github:api",
	"groqapi":    "groq:api",
}

// MigrateCompactID rewrites one ID from flat prefix form to brand:method form.
// "geminiapi:01" → "gemini:api:01". Named IDs (claude:web:ninhle) and custom
// names pass through unchanged. Returns (newID, changed).
func MigrateCompactID(id string) (string, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return id, false
	}
	// Bare / unnumbered OpenRouter → first slot (multi-key uses :02, :03, …).
	if id == "openrouter" || id == "openrouter:api" {
		return "openrouter:api:01", true
	}
	prefix, n, ok := ParseID(id)
	if !ok {
		// Try old flat parse: only one segment before digits.
		old := regexp.MustCompile(`^([a-z0-9]+):([0-9]{2,})$`)
		m := old.FindStringSubmatch(id)
		if m == nil {
			return id, false
		}
		prefix = m[1]
		var err error
		n, err = strconv.Atoi(m[2])
		if err != nil {
			return id, false
		}
	}
	newPrefix, hit := CompactPrefixMigrate[prefix]
	if !hit {
		return id, false
	}
	return FormatID(newPrefix, n), true
}

// RemapAccountIDsInText rewrites old compact account IDs embedded in free text
// (events.log messages like "active provider → geminiapi:01").
func RemapAccountIDsInText(s string) string {
	if s == "" {
		return s
	}
	out := s
	for old, newP := range CompactPrefixMigrate {
		re := regexp.MustCompile(`\b` + regexp.QuoteMeta(old) + `:([0-9]{2,})\b`)
		out = re.ReplaceAllStringFunc(out, func(m string) string {
			sub := re.FindStringSubmatch(m)
			if len(sub) < 2 {
				return m
			}
			n, err := strconv.Atoi(sub[1])
			if err != nil {
				return m
			}
			return FormatID(newP, n)
		})
	}
	// openrouter | openrouter:api | openrouter:api:NN (RE2: no lookahead)
	reOR := regexp.MustCompile(`\bopenrouter(?::api(?::[0-9]{2,})?)?\b`)
	out = reOR.ReplaceAllStringFunc(out, func(m string) string {
		if prefix, _, ok := ParseID(m); ok && prefix == "openrouter:api" {
			return m
		}
		return "openrouter:api:01"
	})
	return out
}

// DisplayAccountID returns the migrated form of an account ID for UI display.
func DisplayAccountID(id string) string {
	if newID, ok := MigrateCompactID(id); ok {
		return newID
	}
	return id
}
