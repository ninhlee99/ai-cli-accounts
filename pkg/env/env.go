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
// ANTHROPIC_BASE_URL is only exported when the proxy is actually reachable
// right now. It used to also fire whenever the user merely had a saved
// claude profile (hasProfiles), regardless of whether the daemon was up —
// so a plain new shell opened while the proxy was down (its normal resting
// state between Claude Code sessions, see `am hook status`) still pointed
// ANTHROPIC_BASE_URL at a dead port instead of falling through to the real
// Anthropic API. hasProfiles is kept as a parameter (unused for the
// override decision) so callers don't need to change; it's still useful
// context for future auto-start behavior.
func PrintEnvExports(proxyUp bool, hasProfiles bool, proxyBase string) {
	if proxyUp {
		fmt.Printf("export ANTHROPIC_BASE_URL=%s\n", proxyBase)
	}
	m := LoadEnvVars()
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		fmt.Printf("export %s=%s\n", k, ShellQuote(m[k]))
	}
}
