// Command am (project ai-cli-accounts) snapshots and swaps the local login state
// of AI CLIs (Claude Code, Codex, Gemini), and can run a local proxy that
// rotates Claude accounts automatically before a rate limit interrupts work.
package main

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
)

func baseDir() string {
	if d := os.Getenv("AM_HOME"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".am")
}

func currentUser() string {
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return os.Getenv("USER")
}

func die(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "am: "+format+"\n", a...)
	os.Exit(1)
}

func usage() {
	fmt.Print(`am - AI CLI account manager

Setup (once):
  am save claude            snapshot each account you want in rotation
                            (log into the next one in Claude, run again)
  am hook install           wire the proxy to Claude Code's start/stop hooks
                            and add ANTHROPIC_BASE_URL to your shell rc

After that use 'claude' normally. The proxy starts with your first session,
swaps to another account before a rate limit stops you (no restart), and
stops itself when the last session ends.

  am ls [tool]              list saved profiles (and which is active)
  am current [tool]         show the account each tool is logged in as
  am status                 proxy state: active account, limits, switches
  am save <tool> [name]     snapshot current login (name defaults to the email)
  am use    <tool> <name>   restore a profile on disk
  am switch <tool> <name>   switch account now — no restart
  am rm   <tool> <name>     delete a profile
  am add  <tool> <name>     log in fresh, then 'am save'

  am hook install|uninstall|status
  am proxy                  run the proxy in the foreground (normally automatic)

  am export [tool] [name..] encrypted blob of profiles for another machine
  am import [--file f] [--activate tool=name]

tools: claude (proxy rotation), codex, gemini (profile swap only)
profiles are encrypted with a master key held in the macOS Keychain.
`)
}

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		usage()
		return
	}
	switch args[0] {
	case "ls", "list":
		cmdLs(args[1:])
	case "current", "now", "who":
		cmdNow(args[1:])
	case "save":
		need(args, 2)
		cmdSave(args[1], arg(args, 2)) // name optional -> account/email
	case "use":
		need(args, 3)
		cmdUse(args[1], resolveName(args[1], args[2]))
	case "switch", "sw":
		need(args, 3)
		cmdSwitch(args[1], resolveName(args[1], args[2]))
	case "rm", "delete":
		need(args, 3)
		cmdRm(args[1], resolveName(args[1], args[2]))
	case "add":
		need(args, 3)
		cmdAdd(args[1], args[2])
	case "hook":
		cmdHook(args[1:])
	case "proxy":
		cmdProxy(args[1:])
	case "status", "st":
		cmdStatus()
	case "export":
		cmdExport(args[1:])
	case "import":
		cmdImport(args[1:])
	case "-h", "--help", "help":
		usage()
	default:
		die("unknown command %q (try: am help)", args[0])
	}
}

func need(args []string, n int) {
	if len(args) < n {
		die("not enough arguments (try: am help)")
	}
}

func arg(args []string, i int) string {
	if i < len(args) {
		return args[i]
	}
	return ""
}

// resolveName lets the user pass a partial profile name (or the account's
// email / id). Exact match wins; otherwise a unique case-insensitive substring
// match of the profile name or its account. Ambiguous or missing -> error.
func resolveName(tool, q string) string {
	profs := listProfiles(tool)
	for _, p := range profs {
		if p.Name == q {
			return q
		}
	}
	ql := strings.ToLower(q)
	var hits []string
	for _, p := range profs {
		if strings.Contains(strings.ToLower(p.Name), ql) ||
			(p.Account != "" && strings.Contains(strings.ToLower(p.Account), ql)) {
			hits = append(hits, p.Name)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0]
	case 0:
		die("no %s profile matching %q (see: am ls %s)", tool, q, tool)
	default:
		die("%q matches %d %s profiles: %s", q, len(hits), tool, strings.Join(hits, ", "))
	}
	return q
}

func toolSpec(name string) ToolSpec {
	c := loadConfig()
	t, ok := c.Tools[name]
	if !ok {
		die("unknown tool %q (known: %s)", name, strings.Join(toolNames(c), ", "))
	}
	return t
}

func toolNames(c Config) []string {
	var n []string
	for k := range c.Tools {
		n = append(n, k)
	}
	sort.Strings(n)
	return n
}

func cmdLs(args []string) {
	c := loadConfig()
	tools := toolNames(c)
	if len(args) > 0 {
		tools = []string{args[0]}
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "TOOL\tPROFILE\tACTIVE\tACCOUNT\tSAVED")
	for _, tn := range tools {
		active := readActivePointer(tn)
		profs := listProfiles(tn)
		if len(profs) == 0 {
			fmt.Fprintf(w, "%s\t-\t\t\t\n", tn)
			continue
		}
		for _, p := range profs {
			mark := ""
			if p.Name == active {
				mark = "*"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", tn, p.Name, mark, p.Account, p.Saved.Format("2006-01-02 15:04"))
		}
	}
	w.Flush()
}

func cmdNow(args []string) {
	c := loadConfig()
	tools := toolNames(c)
	if len(args) > 0 {
		tools = []string{args[0]}
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "TOOL\tLOGGED IN AS\tMATCHES PROFILE")
	for _, tn := range tools {
		acct := detectAccount(toolSpec(tn))
		match := matchProfileByAccount(tn, acct)
		fmt.Fprintf(w, "%s\t%s\t%s\n", tn, orDash(acct), orDash(match))
	}
	w.Flush()
}

func cmdAdd(tool, name string) {
	fmt.Printf(`Log in to %s now in another terminal (its normal login flow), then press Enter.
`, tool)
	fmt.Scanln()
	cmdSave(tool, name)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
