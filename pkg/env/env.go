package env

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"amux-accounts/pkg/types"
)

func EnvPath() string { return filepath.Join(types.BaseDir(), "env.json") }

func LoadEnvVars() map[string]string {
	m := map[string]string{}
	b, err := os.ReadFile(EnvPath())
	if err != nil {
		return m
	}
	_ = json.Unmarshal(b, &m)
	return m
}

func SaveEnvVars(m map[string]string) error {
	_ = os.MkdirAll(types.BaseDir(), 0o700)
	b, _ := json.MarshalIndent(m, "", "  ")
	return os.WriteFile(EnvPath(), append(b, '\n'), 0o600)
}

func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// PrintEnvExports outputs shell export lines for eval "$(am env)".
//
// When the proxy is up, exports the same pair Claude Code expects for a
// custom Anthropic gateway / API-key style setup:
//
//	ANTHROPIC_BASE_URL   → local proxy (instead of api.anthropic.com)
//	ANTHROPIC_AUTH_TOKEN → dummy gateway credential (Authorization: Bearer)
//
// Claude Code keeps owning tools (Bash/Read/…); the proxy only serves
// /v1/messages like Anthropic. ANTHROPIC_BASE_URL is omitted when the
// proxy is down so a new shell falls through to the real API.
func PrintEnvExports(proxyUp bool, hasProfiles bool, proxyBase string) {
	m := LoadEnvVars()

	if proxyUp {
		fmt.Printf("export ANTHROPIC_BASE_URL=%s\n", proxyBase)
		fmt.Printf("export ANTHROPIC_AUTH_TOKEN=am-proxy\n")
		fmt.Printf("export GEMINI_API_BASE=%s\n", proxyBase)
		fmt.Printf("export GOOGLE_GENAI_BASE_URL=%s\n", proxyBase)
		fmt.Printf("alias agy='am run agy'\n")
		fmt.Printf("alias antigravity='am run antigravity'\n")
	} else {
		// A shell that already ran `eval "$(am env)"` while the proxy was up
		// has these exported in its live session. Omitting the line here
		// (old behavior) left them stale once the proxy went down — the
		// shell kept pointing at a dead port instead of falling through to
		// api.anthropic.com or upstream Google. Unset explicitly, unless the
		// user has their own override for these names via `am env set`.
		if _, ok := m["ANTHROPIC_BASE_URL"]; !ok {
			fmt.Printf("unset ANTHROPIC_BASE_URL\n")
		}
		if _, ok := m["ANTHROPIC_AUTH_TOKEN"]; !ok {
			fmt.Printf("unset ANTHROPIC_AUTH_TOKEN\n")
		}
		if _, ok := m["GEMINI_API_BASE"]; !ok {
			fmt.Printf("unset GEMINI_API_BASE\n")
		}
		if _, ok := m["GOOGLE_GENAI_BASE_URL"]; !ok {
			fmt.Printf("unset GOOGLE_GENAI_BASE_URL\n")
		}
		fmt.Printf("unalias agy 2>/dev/null || true\n")
		fmt.Printf("unalias antigravity 2>/dev/null || true\n")
	}

	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		// Don't override the gateway token we just set for the live proxy.
		if proxyUp && (k == "ANTHROPIC_AUTH_TOKEN" || k == "ANTHROPIC_BASE_URL" || k == "GEMINI_API_BASE" || k == "GOOGLE_GENAI_BASE_URL") {
			continue
		}
		fmt.Printf("export %s=%s\n", k, ShellQuote(m[k]))
	}
}
