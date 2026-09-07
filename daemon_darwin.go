package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func splitLines(s string) []string   { return strings.Split(s, "\n") }
func trimSpace(s string) string      { return strings.TrimSpace(s) }
func containsAny(s string, subs ...string) bool {
	for _, x := range subs {
		if strings.Contains(s, x) {
			return true
		}
	}
	return false
}

// The proxy is meant to run continuously in the background so that plain
// `claude` — with ANTHROPIC_BASE_URL pointed at it — always has something to
// talk to. On macOS that's a LaunchAgent with KeepAlive.

const launchLabel = "com.ai-cli-accounts.proxy"

func launchPlistPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", launchLabel+".plist")
}

func cmdDaemon(args []string) {
	sub := "status"
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "install":
		daemonInstall()
	case "uninstall", "remove":
		daemonUninstall()
	case "status":
		daemonStatus()
	case "restart":
		daemonUninstall()
		daemonInstall()
	default:
		die("am daemon: install | uninstall | status | restart")
	}
}

func daemonInstall() {
	self, err := os.Executable()
	if err != nil {
		die("locate self: %v", err)
	}
	addr := envOr("AM_PROXY_ADDR", "127.0.0.1:8787")
	logPath := filepath.Join(baseDir(), "proxy.log")

	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>proxy</string>
    <string>--addr</string>
    <string>%s</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>%s</string>
  <key>StandardErrorPath</key><string>%s</string>
  <key>ProcessType</key><string>Background</string>
</dict>
</plist>
`, launchLabel, self, addr, logPath, logPath)

	p := launchPlistPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		die("mkdir LaunchAgents: %v", err)
	}
	if err := os.WriteFile(p, []byte(plist), 0o644); err != nil {
		die("write plist: %v", err)
	}
	// bootout first in case an old one is loaded, then bootstrap.
	uid := fmt.Sprintf("gui/%d", os.Getuid())
	_ = exec.Command("launchctl", "bootout", uid+"/"+launchLabel).Run()
	if out, err := exec.Command("launchctl", "bootstrap", uid, p).CombinedOutput(); err != nil {
		die("launchctl bootstrap: %v\n%s", err, out)
	}
	fmt.Printf("installed LaunchAgent %s\n  plist: %s\n  proxy: http://%s\n", launchLabel, p, addr)
	fmt.Printf("\nadd to ~/.zshrc so plain `claude` uses it:\n  export ANTHROPIC_BASE_URL=http://%s\n", addr)
}

func daemonUninstall() {
	uid := fmt.Sprintf("gui/%d", os.Getuid())
	_ = exec.Command("launchctl", "bootout", uid+"/"+launchLabel).Run()
	if err := os.Remove(launchPlistPath()); err != nil && !os.IsNotExist(err) {
		die("remove plist: %v", err)
	}
	fmt.Printf("uninstalled LaunchAgent %s\n", launchLabel)
}

func daemonStatus() {
	p := launchPlistPath()
	if _, err := os.Stat(p); err != nil {
		fmt.Println("LaunchAgent: not installed  (am daemon install)")
	} else {
		fmt.Printf("LaunchAgent: installed (%s)\n", p)
		out, _ := exec.Command("launchctl", "print", fmt.Sprintf("gui/%d/%s", os.Getuid(), launchLabel)).CombinedOutput()
		for _, line := range splitLines(string(out)) {
			if containsAny(line, "state =", "pid =", "last exit") {
				fmt.Println("  " + trimSpace(line))
			}
		}
	}
	addr := envOr("AM_PROXY_ADDR", "127.0.0.1:8787")
	if proxyUp("http://" + addr) {
		fmt.Printf("proxy: responding on http://%s\n", addr)
	} else {
		fmt.Printf("proxy: NOT responding on http://%s\n", addr)
	}
	if os.Getenv("ANTHROPIC_BASE_URL") == "" {
		fmt.Println("ANTHROPIC_BASE_URL: not set — plain `claude` will bypass the proxy")
	} else {
		fmt.Printf("ANTHROPIC_BASE_URL: %s\n", os.Getenv("ANTHROPIC_BASE_URL"))
	}
}
