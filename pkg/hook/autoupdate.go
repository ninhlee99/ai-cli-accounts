package hook

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"amux-accounts/pkg/types"
)

const AutoUpdatePlistLabel = "com.ninhlee.amux.autoupdate"

// LaunchAgentPath returns the macOS LaunchAgent plist path for amux auto-update.
func LaunchAgentPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", AutoUpdatePlistLabel+".plist")
}

// AutoUpdateConfig records the current auto-update state and configuration.
type AutoUpdateConfig struct {
	Enabled       bool   `json:"enabled"`
	IntervalHours int    `json:"interval_hours"`
	PlistPath     string `json:"plist_path,omitempty"`
}

// AutoUpdateConfigFile returns the path to ~/.am/autoupdate.json.
func AutoUpdateConfigFile() string {
	return filepath.Join(types.BaseDir(), "autoupdate.json")
}

// IsAutoUpdateEnabled reports whether auto-update is currently configured and active.
func IsAutoUpdateEnabled() bool {
	b, err := os.ReadFile(AutoUpdateConfigFile())
	if err != nil {
		if _, err := os.Stat(LaunchAgentPath()); err == nil {
			return true
		}
		return false
	}
	var cfg AutoUpdateConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		return false
	}
	return cfg.Enabled
}

// SetupAutoUpdate enables or disables automatic updates via macOS LaunchAgent.
func SetupAutoUpdate(enable bool) error {
	baseDir := types.BaseDir()
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return fmt.Errorf("mkdir base dir: %w", err)
	}

	plistPath := LaunchAgentPath()

	if !enable {
		if runtime.GOOS == "darwin" {
			_ = exec.Command("launchctl", "unload", plistPath).Run()
		}
		_ = os.Remove(plistPath)
		data, _ := json.MarshalIndent(AutoUpdateConfig{Enabled: false, IntervalHours: 6}, "", "  ")
		_ = os.WriteFile(AutoUpdateConfigFile(), data, 0o644)
		return nil
	}

	binPath := ""
	home, _ := os.UserHomeDir()
	candidates := []string{
		filepath.Join(home, ".local", "bin", "amux"),
		filepath.Join(home, ".local", "bin", "am"),
		"/usr/local/bin/amux",
		"/usr/local/bin/am",
	}
	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && !fi.IsDir() {
			binPath = c
			break
		}
	}
	if binPath == "" {
		if p, err := exec.LookPath("amux"); err == nil {
			binPath = p
		} else if p, err := exec.LookPath("am"); err == nil {
			binPath = p
		} else if exe, err := os.Executable(); err == nil {
			binPath = exe
		}
	}

	if runtime.GOOS == "darwin" {
		plistDir := filepath.Dir(plistPath)
		if err := os.MkdirAll(plistDir, 0o755); err != nil {
			return fmt.Errorf("mkdir LaunchAgents: %w", err)
		}

		logPath := filepath.Join(baseDir, "autoupdate.log")

		// 21600 seconds = every 6 hours
		plistContent := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>update</string>
		<string>--quiet</string>
	</array>
	<key>StartInterval</key>
	<integer>21600</integer>
	<key>RunAtLoad</key>
	<false/>
	<key>StandardOutPath</key>
	<string>%s</string>
	<key>StandardErrorPath</key>
	<string>%s</string>
</dict>
</plist>
`, AutoUpdatePlistLabel, binPath, logPath, logPath)

		_ = exec.Command("launchctl", "unload", plistPath).Run()
		if err := os.WriteFile(plistPath, []byte(plistContent), 0o644); err != nil {
			return fmt.Errorf("write plist: %w", err)
		}
		_ = exec.Command("launchctl", "load", plistPath).Run()
	}

	cfg := AutoUpdateConfig{
		Enabled:       true,
		IntervalHours: 6,
		PlistPath:     plistPath,
	}
	data, _ := json.MarshalIndent(cfg, "", "  ")
	_ = os.WriteFile(AutoUpdateConfigFile(), data, 0o644)
	return nil
}
