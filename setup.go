package main

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
)

// feedbackSlashCmd is the /am:feedback command definition, embedded so `am
// setup` can install it globally even after the source repo is gone (the
// binary is typically moved to /usr/local/bin and the clone deleted).
//
//go:embed commands/feedback.md
var feedbackSlashCmd []byte

// cmdSetup does the one-time onboarding: wires the Claude Code hook (proxy
// autostart/stop + ANTHROPIC_BASE_URL) and installs the /am:feedback slash
// command globally, so both work from any project, not just this repo.
func cmdSetup(args []string) {
	fmt.Println("== hook ==")
	hookInstall()

	fmt.Println("\n== /am:feedback slash command ==")
	installSlashCommand("feedback.md", feedbackSlashCmd)

	fmt.Println("\nsetup done. Open a new shell, then: am add   (save your first account)")
}

// installSlashCommand writes one command file under the user's global
// ~/.claude/commands/am/, so it's available in every project.
func installSlashCommand(name string, content []byte) {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "am: couldn't resolve home dir, skipping slash command: %v\n", err)
		return
	}
	dir := filepath.Join(home, ".claude", "commands", "am")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "am: mkdir %s: %v\n", dir, err)
		return
	}
	dst := filepath.Join(dir, name)
	if err := os.WriteFile(dst, content, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "am: write %s: %v\n", dst, err)
		return
	}
	fmt.Printf("installed %s -> /am:%s\n", dst, name[:len(name)-len(filepath.Ext(name))])
}
