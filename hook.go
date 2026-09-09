package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The proxy is started on demand by a Claude Code lifecycle hook and then
// runs indefinitely (see runProxyForeground) so a still-open tab is never
// left pointed at a dead port. `am hook install` wires:
//
//   SessionStart -> am proxy up     (spawn if needed, register the session)
//   SessionEnd   -> am proxy down   (deregister; just updates `am status`'s count)
//
// It also needs ANTHROPIC_BASE_URL to point `claude` at the proxy. Hooks can't
// set session env, so `am hook install` offers to append one line to the
// shell rc: `eval "$(am env)"`, which resolves ANTHROPIC_BASE_URL (and any
// vars from `am env set`) fresh in every new shell — see env.go.

func claudeSettingsPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "settings.json")
}

func cmdHook(args []string) {
	sub := "status"
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "install":
		hookInstall()
	case "uninstall", "remove":
		hookUninstall()
	case "status":
		hookStatus()
	default:
		die("am hook: install | uninstall | status")
	}
}

type hookCmd struct {
	Type    string `json:"type"`
	Command string `json:"command"`
}
type hookEntry struct {
	Hooks []hookCmd `json:"hooks"`
}

const hookTag = "am proxy" // how we recognise our own entries

func loadClaudeSettings() map[string]any {
	b, err := os.ReadFile(claudeSettingsPath())
	if err != nil {
		return map[string]any{}
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		die("cannot parse %s", claudeSettingsPath())
	}
	return m
}

func saveClaudeSettings(m map[string]any) {
	if err := os.MkdirAll(filepath.Dir(claudeSettingsPath()), 0o755); err != nil {
		die("mkdir .claude: %v", err)
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(claudeSettingsPath(), append(b, '\n'), 0o600); err != nil {
		die("write settings: %v", err)
	}
}

// addHook appends our command to settings["hooks"][event], leaving any other
// entries (and other events) untouched.
func addHook(m map[string]any, event, command string) {
	hooks, _ := m["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
		m["hooks"] = hooks
	}
	list, _ := hooks[event].([]any)
	// drop a previous version of ours
	var kept []any
	for _, e := range list {
		if !hookEntryIsOurs(e) {
			kept = append(kept, e)
		}
	}
	entry := map[string]any{
		"hooks": []any{map[string]any{"type": "command", "command": command}},
	}
	hooks[event] = append(kept, entry)
}

func hookEntryIsOurs(e any) bool {
	m, ok := e.(map[string]any)
	if !ok {
		return false
	}
	hs, _ := m["hooks"].([]any)
	for _, h := range hs {
		hm, _ := h.(map[string]any)
		if c, _ := hm["command"].(string); strings.Contains(c, hookTag) {
			return true
		}
	}
	return false
}

func removeOurHooks(m map[string]any) int {
	hooks, _ := m["hooks"].(map[string]any)
	if hooks == nil {
		return 0
	}
	n := 0
	for event, v := range hooks {
		list, _ := v.([]any)
		var kept []any
		for _, e := range list {
			if hookEntryIsOurs(e) {
				n++
			} else {
				kept = append(kept, e)
			}
		}
		if len(kept) == 0 {
			delete(hooks, event)
		} else {
			hooks[event] = kept
		}
	}
	if len(hooks) == 0 {
		delete(m, "hooks")
	}
	return n
}

func hookInstall() {
	self, err := os.Executable()
	if err != nil {
		die("locate self: %v", err)
	}
	m := loadClaudeSettings()
	addHook(m, "SessionStart", fmt.Sprintf("%q proxy up", self))
	addHook(m, "SessionEnd", fmt.Sprintf("%q proxy down", self))
	saveClaudeSettings(m)

	fmt.Printf("installed hooks in %s\n  SessionStart -> am proxy up\n  SessionEnd   -> am proxy down\n\n", claudeSettingsPath())

	// `am env` resolves ANTHROPIC_BASE_URL dynamically (always the proxy),
	// so the rc line never goes stale even if the proxy's addr ever changes.
	line := `eval "$(am env)"`
	rc := shellRC()
	if rc != "" && rcHasLine(rc, line) {
		fmt.Println("shell rc already wired to `am env` — done.")
		return
	}
	if rc != "" && !rcHasLine(rc, line) {
		fmt.Printf("add this to %s (once):\n  %s\n", rc, line)
		fmt.Print("append it now? [y/N] ")
		var ans string
		fmt.Scanln(&ans)
		if strings.EqualFold(strings.TrimSpace(ans), "y") {
			appendLine(rc, "\n# ai-cli-accounts: route claude through the rotating proxy\n"+line+"\n")
			fmt.Printf("appended. open a new shell (or `source %s`).\n", rc)
			return
		}
	}
	fmt.Printf("then open a new shell.\n")
}

func hookUninstall() {
	m := loadClaudeSettings()
	n := removeOurHooks(m)
	saveClaudeSettings(m)
	fmt.Printf("removed %d hook entr%s from %s\n", n, plural(n, "y", "ies"), claudeSettingsPath())
	fmt.Printf("also remove the `eval \"$(am env)\"` line from your shell rc if you added it.\n")
}

func hookStatus() {
	m := loadClaudeSettings()
	hooks, _ := m["hooks"].(map[string]any)
	found := false
	for event, v := range hooks {
		list, _ := v.([]any)
		for _, e := range list {
			if hookEntryIsOurs(e) {
				fmt.Printf("hook: %s -> am proxy\n", event)
				found = true
			}
		}
	}
	if !found {
		fmt.Println("hooks: not installed  (am hook install)")
	}
	switch os.Getenv("ANTHROPIC_BASE_URL") {
	case proxyBase():
		fmt.Printf("ANTHROPIC_BASE_URL: %s\n", proxyBase())
	case "":
		fmt.Println("ANTHROPIC_BASE_URL: not set — claude will bypass the proxy (run `am hook install` to wire `am env`)")
	default:
		fmt.Printf("ANTHROPIC_BASE_URL: %s  (not the proxy)\n", os.Getenv("ANTHROPIC_BASE_URL"))
	}
	if proxyUp() {
		fmt.Println("proxy: running")
	} else {
		fmt.Println("proxy: not running (normal when no claude session is open)")
	}
}

func shellRC() string {
	home, _ := os.UserHomeDir()
	switch filepath.Base(os.Getenv("SHELL")) {
	case "zsh":
		return filepath.Join(home, ".zshrc")
	case "bash":
		return filepath.Join(home, ".bashrc")
	default:
		return ""
	}
}

func rcHasLine(path, line string) bool {
	b, err := os.ReadFile(path)
	return err == nil && strings.Contains(string(b), line)
}

func appendLine(path, text string) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		die("open %s: %v", path, err)
	}
	defer f.Close()
	if _, err := f.WriteString(text); err != nil {
		die("append %s: %v", path, err)
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
