package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// am env prints `export KEY=VALUE` lines meant to be eval'd from the shell rc
// (`eval "$(am env)"`), so ANTHROPIC_BASE_URL always reflects whatever's true
// right now — pointing at the proxy — instead of a value hardcoded once and
// left stale in .zshrc. `am env set/get/rm/list` manage arbitrary extra vars
// stored alongside it, e.g. for keys you always want exported in every shell.

func envPath() string { return filepath.Join(baseDir(), "env.json") }

func loadEnvVars() map[string]string {
	m := map[string]string{}
	b, err := os.ReadFile(envPath())
	if err != nil {
		return m
	}
	_ = json.Unmarshal(b, &m)
	return m
}

func saveEnvVars(m map[string]string) {
	_ = os.MkdirAll(baseDir(), 0o700)
	b, _ := json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(envPath(), append(b, '\n'), 0o600); err != nil {
		die("save env vars: %v", err)
	}
}

func cmdEnv(args []string) {
	if len(args) == 0 {
		printEnvExports()
		return
	}
	switch args[0] {
	case "set":
		if len(args) < 3 {
			die("am env set KEY VALUE")
		}
		m := loadEnvVars()
		m[args[1]] = args[2]
		saveEnvVars(m)
		fmt.Fprintf(os.Stderr, "set %s (run `eval \"$(am env)\"` or open a new shell)\n", args[1])
	case "get":
		if len(args) < 2 {
			die("am env get KEY")
		}
		v, ok := loadEnvVars()[args[1]]
		if !ok {
			die("%s not set", args[1])
		}
		fmt.Println(v)
	case "rm", "unset":
		if len(args) < 2 {
			die("am env rm KEY")
		}
		m := loadEnvVars()
		delete(m, args[1])
		saveEnvVars(m)
		fmt.Fprintf(os.Stderr, "removed %s (run `eval \"$(am env)\"` or open a new shell)\n", args[1])
	case "list", "ls":
		m := loadEnvVars()
		names := make([]string, 0, len(m))
		for k := range m {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			fmt.Printf("%s=%s\n", k, m[k])
		}
	default:
		die("am env: [set KEY VALUE | get KEY | rm KEY | list] (no args prints shell exports)")
	}
}

// printEnvExports is what `eval "$(am env)"` in .zshrc actually runs.
//
// If the proxy is already up (the common case — SessionStart from an earlier
// tab keeps it running now that it never self-stops), point at it. If it's
// down but claude profiles exist, still point at it: SessionStart will bring
// it up before claude's first request. Only skip ANTHROPIC_BASE_URL — letting
// claude fall back to the real Anthropic API — when there's nothing for the
// proxy to serve yet (no profiles saved), so a shell opened before `am add`
// or `am hook install` never ends up pointed at a port nothing can start.
func printEnvExports() {
	if proxyUp() || len(listProfiles("claude")) > 0 {
		fmt.Printf("export ANTHROPIC_BASE_URL=%s\n", proxyBase())
	}
	m := loadEnvVars()
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		fmt.Printf("export %s=%s\n", k, shellQuote(m[k]))
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
