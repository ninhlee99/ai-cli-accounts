package hook

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// The proxy is started on demand by a Claude Code lifecycle hook and then
// runs indefinitely so a still-open tab is never left pointed at a dead
// port. `am hook install` wires:
//
//   SessionStart -> am proxy up     (spawn if needed, register the session)
//   SessionEnd   -> am proxy down   (deregister; just updates `am status`'s count)
//   Stop         -> am proxy down   (extra safety net if SessionEnd is missed)
//
// It also needs ANTHROPIC_BASE_URL to point `claude` at the proxy. Hooks
// can't set session env, so `am hook install` offers to append one line to
// the shell rc: `eval "$(am env)"`, which resolves ANTHROPIC_BASE_URL (and
// any vars from `am env set`) fresh in every new shell — see pkg/env.

const HookTag = "am proxy" // how we recognise our own entries

// CanonicalTool normalizes user input into one of: claude, agy, codex, cursor.
// Returns empty string if unknown.
func CanonicalTool(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "claude", "claudecode", "cc":
		return "claude"
	case "agy", "antigravity", "gemini":
		return "agy"
	case "codex":
		return "codex"
	case "cursor":
		return "cursor"
	default:
		return ""
	}
}

func ClaudeAvailable() bool {
	home, _ := os.UserHomeDir()
	if _, err := os.Stat(filepath.Join(home, ".claude")); err == nil {
		return true
	}
	_, err := exec.LookPath("claude")
	return err == nil
}

func GeminiAvailable() bool {
	home, _ := os.UserHomeDir()
	if _, err := os.Stat(filepath.Join(home, ".gemini")); err == nil {
		return true
	}
	_, err := exec.LookPath("agy")
	return err == nil
}

func CodexAvailable() bool {
	home, _ := os.UserHomeDir()
	if _, err := os.Stat(filepath.Join(home, ".codex")); err == nil {
		return true
	}
	_, err := exec.LookPath("codex")
	return err == nil
}

func CursorAvailable() bool {
	home, _ := os.UserHomeDir()
	if _, err := os.Stat(filepath.Join(home, ".cursor")); err == nil {
		return true
	}
	_, err := exec.LookPath("cursor")
	return err == nil
}

func ClaudeSettingsPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "settings.json")
}

func LoadClaudeSettings() map[string]any {
	b, err := os.ReadFile(ClaudeSettingsPath())
	if err != nil {
		return map[string]any{}
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return map[string]any{}
	}
	return m
}

func SaveClaudeSettings(m map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(ClaudeSettingsPath()), 0o755); err != nil {
		return fmt.Errorf("mkdir .claude: %w", err)
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	return os.WriteFile(ClaudeSettingsPath(), append(b, '\n'), 0o600)
}

// AddHook appends our command to settings["hooks"][event], leaving any other
// entries (and other events) untouched. A previous version of our own entry
// (however it was formatted) is dropped first so re-running `install` never
// piles up duplicates.
func AddHook(m map[string]any, event, command string) {
	hooks, _ := m["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
		m["hooks"] = hooks
	}
	list, _ := hooks[event].([]any)
	var kept []any
	for _, e := range list {
		if !HookEntryIsOurs(e) {
			kept = append(kept, e)
		}
	}
	entry := map[string]any{
		"hooks": []any{map[string]any{"type": "command", "command": command}},
	}
	hooks[event] = append(kept, entry)
}

// HookEntryIsOurs recognises our own hook entries even across the command
// formats we've used historically: a bare "am proxy up"/"am proxy down", or
// (older installs) a quoted absolute path to the binary — `"<path>/am"
// proxy up` — which doesn't contain the literal "am proxy" substring
// because of the closing quote, hence the extra checks.
func isOurHookCommand(c string) bool {
	return strings.Contains(c, HookTag) ||
		strings.Contains(c, "proxy up") ||
		strings.Contains(c, "proxy down") ||
		strings.Contains(c, "hook claude") ||
		strings.Contains(c, "hook agy") ||
		strings.Contains(c, "hook codex") ||
		strings.Contains(c, "hook cursor")
}

func HookEntryIsOurs(e any) bool {
	m, ok := e.(map[string]any)
	if !ok {
		return false
	}
	if hooks, ok := m["hooks"].([]any); ok {
		for _, h := range hooks {
			hm, ok := h.(map[string]any)
			if !ok {
				continue
			}
			c, _ := hm["command"].(string)
			if isOurHookCommand(c) {
				return true
			}
		}
	}
	if c, ok := m["command"].(string); ok {
		if isOurHookCommand(c) {
			return true
		}
	}
	return false
}

// HookInstall wires SessionStart/SessionEnd/Stop hooks to the currently
// running `am` binary's resolved absolute path (quoted, so it survives
// paths with spaces) — this way the hook keeps working even if `am` isn't
// on PATH in whatever minimal environment Claude Code invokes hooks in.
func HookInstall() error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate self: %w", err)
	}
	m := LoadClaudeSettings()
	AddHook(m, "SessionStart", fmt.Sprintf("%q hook claude start", self))
	AddHook(m, "SessionEnd", fmt.Sprintf("%q hook claude stop", self))
	AddHook(m, "Stop", fmt.Sprintf("%q hook claude stop", self))
	return SaveClaudeSettings(m)
}

func HookUninstall() (int, error) {
	m := LoadClaudeSettings()
	hooks, _ := m["hooks"].(map[string]any)
	n := 0
	if hooks != nil {
		for event, listRaw := range hooks {
			list, ok := listRaw.([]any)
			if !ok {
				continue
			}
			var kept []any
			for _, e := range list {
				if HookEntryIsOurs(e) {
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
	}
	return n, SaveClaudeSettings(m)
}

func HookInstalled() bool {
	m := LoadClaudeSettings()
	hooks, _ := m["hooks"].(map[string]any)
	if hooks == nil {
		return false
	}
	start, _ := hooks["SessionStart"].([]any)
	for _, e := range start {
		if HookEntryIsOurs(e) {
			return true
		}
	}
	return false
}

// InstalledEvents returns the hook events we currently own an entry in,
// e.g. ["SessionStart", "SessionEnd", "Stop"].
func InstalledEvents() []string {
	m := LoadClaudeSettings()
	hooks, _ := m["hooks"].(map[string]any)
	var events []string
	for event, v := range hooks {
		list, _ := v.([]any)
		for _, e := range list {
			if HookEntryIsOurs(e) {
				events = append(events, event)
				break
			}
		}
	}
	return events
}

// claudeSettingsEnvKeys are the vars we own inside settings["env"] — never
// touch anything else a user put there themselves.
var claudeSettingsEnvKeys = []string{"ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN"}

// SyncClaudeSettingsEnv mirrors the proxy's reachability into
// ~/.claude/settings.json's "env" block, which Claude Code reads at the
// start of every new session — independent of shell rc (`eval "$(am env)"`)
// and launchctl (SyncLaunchctlEnv), both of which only reach processes
// spawned *after* the change. A terminal/session already running when the
// proxy goes up or down won't see this either, but every session opened
// from that point on will, without the user needing to `eval` anything.
//
// proxyUp true  -> set ANTHROPIC_BASE_URL/ANTHROPIC_AUTH_TOKEN to proxyBase.
// proxyUp false -> remove both keys so a new session falls through to the
// real Anthropic API on whatever ANTHROPIC_API_KEY / subscription login it
// already has.
func SyncClaudeSettingsEnv(proxyUp bool, proxyBase string) error {
	m := LoadClaudeSettings()
	env, _ := m["env"].(map[string]any)
	if env == nil {
		env = map[string]any{}
	}
	if proxyUp {
		env["ANTHROPIC_BASE_URL"] = proxyBase
		env["ANTHROPIC_AUTH_TOKEN"] = "am-proxy"
	} else {
		for _, k := range claudeSettingsEnvKeys {
			delete(env, k)
		}
	}
	if len(env) == 0 {
		delete(m, "env")
	} else {
		m["env"] = env
	}
	return SaveClaudeSettings(m)
}

// ShellRC returns the user's shell rc file for zsh/bash, or "" if the shell
// isn't one we know how to wire automatically.
func ShellRC() string {
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

// RCHasLine reports whether path already contains line.
func RCHasLine(path, line string) bool {
	b, err := os.ReadFile(path)
	return err == nil && strings.Contains(string(b), line)
}

// AppendLine appends text to path, creating it if necessary.
func AppendLine(path, text string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	_, err = f.WriteString(text)
	return err
}

func GeminiConfigDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".gemini", "config")
}

func GeminiHooksPath() string {
	return filepath.Join(GeminiConfigDir(), "hooks.json")
}

func LoadGeminiHooks() map[string]any {
	b, err := os.ReadFile(GeminiHooksPath())
	if err != nil {
		return map[string]any{}
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return map[string]any{}
	}
	return m
}

func SaveGeminiHooks(m map[string]any) error {
	p := GeminiHooksPath()
	if len(m) == 0 {
		_ = os.Remove(p)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return fmt.Errorf("mkdir .gemini/config: %w", err)
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	return os.WriteFile(p, append(b, '\n'), 0o600)
}

func GeminiHookInstall() error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate self: %w", err)
	}
	m := LoadGeminiHooks()
	m["amux-proxy"] = map[string]any{
		"SessionStart": []any{
			map[string]any{
				"type":    "command",
				"command": fmt.Sprintf("%q hook agy start", self),
				"timeout": 15,
			},
		},
		"PreInvocation": []any{
			map[string]any{
				"type":    "command",
				"command": fmt.Sprintf("%q hook agy start", self),
				"timeout": 15,
			},
		},
		"Stop": []any{
			map[string]any{
				"type":    "command",
				"command": fmt.Sprintf("%q hook agy stop", self),
				"timeout": 15,
			},
		},
	}
	return SaveGeminiHooks(m)
}

func GeminiHookUninstall() (int, error) {
	m := LoadGeminiHooks()
	if _, ok := m["amux-proxy"]; ok {
		delete(m, "amux-proxy")
		return 1, SaveGeminiHooks(m)
	}
	return 0, nil
}

func GeminiHookInstalled() bool {
	m := LoadGeminiHooks()
	_, ok := m["amux-proxy"]
	return ok
}

func GeminiInstalledEvents() []string {
	m := LoadGeminiHooks()
	spec, ok := m["amux-proxy"].(map[string]any)
	if !ok {
		return nil
	}
	var evs []string
	for ev := range spec {
		evs = append(evs, ev)
	}
	sort.Strings(evs)
	return evs
}

func CodexHooksPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex", "hooks.json")
}

func LoadCodexHooks() map[string]any {
	b, err := os.ReadFile(CodexHooksPath())
	if err != nil {
		return map[string]any{}
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return map[string]any{}
	}
	return m
}

func SaveCodexHooks(m map[string]any) error {
	p := CodexHooksPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return fmt.Errorf("mkdir .codex: %w", err)
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	return os.WriteFile(p, append(b, '\n'), 0o600)
}

func CodexHookInstall() error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate self: %w", err)
	}
	home, _ := os.UserHomeDir()
	if _, err := os.Stat(filepath.Join(home, ".codex")); err != nil {
		return nil
	}
	m := LoadCodexHooks()
	AddHook(m, "SessionStart", fmt.Sprintf("%q hook codex start", self))
	return SaveCodexHooks(m)
}

func CodexHookUninstall() (int, error) {
	home, _ := os.UserHomeDir()
	if _, err := os.Stat(filepath.Join(home, ".codex")); err != nil {
		return 0, nil
	}
	m := LoadCodexHooks()
	hooks, _ := m["hooks"].(map[string]any)
	n := 0
	if hooks != nil {
		for event, listRaw := range hooks {
			list, ok := listRaw.([]any)
			if !ok {
				continue
			}
			var kept []any
			for _, e := range list {
				if HookEntryIsOurs(e) {
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
	}
	return n, SaveCodexHooks(m)
}

func CodexHookInstalled() bool {
	m := LoadCodexHooks()
	hooks, _ := m["hooks"].(map[string]any)
	if hooks == nil {
		return false
	}
	start, _ := hooks["SessionStart"].([]any)
	for _, e := range start {
		if HookEntryIsOurs(e) {
			return true
		}
	}
	return false
}

func CursorHooksPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cursor", "hooks.json")
}

func LoadCursorHooks() map[string]any {
	b, err := os.ReadFile(CursorHooksPath())
	if err != nil {
		return map[string]any{}
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return map[string]any{}
	}
	return m
}

func SaveCursorHooks(m map[string]any) error {
	p := CursorHooksPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return fmt.Errorf("mkdir .cursor: %w", err)
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	return os.WriteFile(p, append(b, '\n'), 0o600)
}

func CursorHookInstall() error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate self: %w", err)
	}
	home, _ := os.UserHomeDir()
	if _, err := os.Stat(filepath.Join(home, ".cursor")); err != nil {
		return nil
	}
	m := LoadCursorHooks()
	hooks, _ := m["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
		m["hooks"] = hooks
	}
	if _, ok := m["version"]; !ok {
		m["version"] = 1
	}
	list, _ := hooks["sessionStart"].([]any)
	var kept []any
	for _, e := range list {
		if em, ok := e.(map[string]any); ok {
			if c, _ := em["command"].(string); isOurHookCommand(c) {
				continue
			}
		}
		kept = append(kept, e)
	}
	entry := map[string]any{"command": fmt.Sprintf("%q hook cursor start", self)}
	hooks["sessionStart"] = append(kept, entry)
	return SaveCursorHooks(m)
}

func CursorHookUninstall() (int, error) {
	home, _ := os.UserHomeDir()
	if _, err := os.Stat(filepath.Join(home, ".cursor")); err != nil {
		return 0, nil
	}
	m := LoadCursorHooks()
	hooks, _ := m["hooks"].(map[string]any)
	n := 0
	if hooks != nil {
		list, _ := hooks["sessionStart"].([]any)
		var kept []any
		for _, e := range list {
			if em, ok := e.(map[string]any); ok {
				if c, _ := em["command"].(string); isOurHookCommand(c) {
					n++
					continue
				}
			}
			kept = append(kept, e)
		}
		if len(kept) == 0 {
			delete(hooks, "sessionStart")
		} else {
			hooks["sessionStart"] = kept
		}
	}
	return n, SaveCursorHooks(m)
}

func CursorHookInstalled() bool {
	m := LoadCursorHooks()
	hooks, _ := m["hooks"].(map[string]any)
	if hooks == nil {
		return false
	}
	list, _ := hooks["sessionStart"].([]any)
	for _, e := range list {
		if em, ok := e.(map[string]any); ok {
			if c, _ := em["command"].(string); isOurHookCommand(c) {
				return true
			}
		}
	}
	return false
}

