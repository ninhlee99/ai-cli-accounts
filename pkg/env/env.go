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
	if proxyUp {
		fmt.Printf("export ANTHROPIC_BASE_URL=%s\n", proxyBase)
		fmt.Printf("export ANTHROPIC_AUTH_TOKEN=am-proxy\n")
	}
	m := LoadEnvVars()
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		// Don't override the gateway token we just set for the live proxy.
		if proxyUp && k == "ANTHROPIC_AUTH_TOKEN" {
			continue
		}
		if proxyUp && k == "ANTHROPIC_BASE_URL" {
			continue
		}
		fmt.Printf("export %s=%s\n", k, ShellQuote(m[k]))
	}
}
