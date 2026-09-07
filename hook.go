package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The proxy is started and stopped by Claude Code lifecycle hooks so it only
// runs while `claude` is running. `am hook install` wires:
//
//   SessionStart -> am proxy up     (spawn if needed, register the session)
//   SessionEnd   -> am proxy down   (deregister; proxy self-stops at zero)
//
// It also needs ANTHROPIC_BASE_URL to point `claude` at the proxy. Hooks can't
// set session env, so that one line still goes in the shell rc — `am hook
// install` prints it and offers to append it.

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

	line := "export ANTHROPIC_BASE_URL=" + proxyBase()
	if os.Getenv("ANTHROPIC_BASE_URL") == proxyBase() {
		fmt.Println("ANTHROPIC_BASE_URL already points at the proxy — done.")
		return
	}
	rc := shellRC()
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
	fmt.Printf("also remove the ANTHROPIC_BASE_URL line from your shell rc if you added it.\n")
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
	if os.Getenv("ANTHROPIC_BASE_URL") == proxyBase() {
		fmt.Printf("ANTHROPIC_BASE_URL: %s\n", proxyBase())
	} else if os.Getenv("ANTHROPIC_BASE_URL") == "" {
		fmt.Println("ANTHROPIC_BASE_URL: not set — claude will bypass the proxy")
	} else {
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
