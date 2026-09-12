package hook

import (
	"os/exec"
	"runtime"
)

// LaunchctlSetenv mirrors a var into the macOS GUI session (launchctl
// setenv), so apps launched outside any shell — Dock icons, IDE
// integrations, editor extensions — inherit it too, not just processes
// spawned from a shell that sourced `eval "$(am env)"`. No-op off darwin.
func LaunchctlSetenv(key, val string) error {
	if runtime.GOOS != "darwin" {
		return nil
	}
	return exec.Command("launchctl", "setenv", key, val).Run()
}

// LaunchctlUnsetenv removes a var set via LaunchctlSetenv. No-op off darwin.
func LaunchctlUnsetenv(key string) error {
	if runtime.GOOS != "darwin" {
		return nil
	}
	return exec.Command("launchctl", "unsetenv", key).Run()
}

// SyncLaunchctlEnv mirrors the proxy's current reachability into the
// launchctl session env, so GUI-launched processes (which never source
// shell rc, see HookInstall) fall back to the real Anthropic API the same
// way a fresh shell does via `am env` — instead of staying pointed at a
// dead local port once the proxy goes down.
//
// proxyUp true  -> setenv ANTHROPIC_BASE_URL/ANTHROPIC_AUTH_TOKEN at proxyBase.
// proxyUp false -> unsetenv both, so claude falls through to api.anthropic.com
// using whatever ANTHROPIC_API_KEY / subscription login it already has.
func SyncLaunchctlEnv(proxyUp bool, proxyBase string) {
	if proxyUp {
		_ = LaunchctlSetenv("ANTHROPIC_BASE_URL", proxyBase)
		_ = LaunchctlSetenv("ANTHROPIC_AUTH_TOKEN", "am-proxy")
		return
	}
	_ = LaunchctlUnsetenv("ANTHROPIC_BASE_URL")
	_ = LaunchctlUnsetenv("ANTHROPIC_AUTH_TOKEN")
}
