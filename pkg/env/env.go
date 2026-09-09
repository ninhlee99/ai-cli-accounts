package env

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"ai-cli-accounts/pkg/types"
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
func PrintEnvExports(proxyUp bool, hasProfiles bool, proxyBase string) {
	if proxyUp || hasProfiles {
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
