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

  am <tool> [args...]       shortcut for 'am up': run the tool, starting the
                            proxy first if needed  (e.g. am claude --continue)

  am ls [tool]              list saved profiles (and which is active)
  am current [tool]         show the account each tool is currently logged in as
  am save <tool> [name]     snapshot the current login (name defaults to the
                            account email, e.g. you@gmail.com)
  am use    <tool> <name>   restore a profile on disk (auto-saves current first)
  am switch <tool> <name>   switch account; live via the proxy if it's running,
                            else same as 'use'
  am rm   <tool> <name>     delete a profile
  am add  <tool> <name>     alias for: log in fresh, then 'am save'
  am proxy [--addr host:port]
                            run the rotating proxy (Claude: auto-switch profile
                            before the rate limit is hit, refresh OAuth tokens)
  am run  <tool> [args...]  exec the tool with env pointed at an already-running
                            proxy (fails if none)
  am up   <tool> [args...]  run the tool, auto-starting the proxy if needed
                            (same as the 'am <tool>' shortcut)

  am export [tool] [name..] print an encrypted, passphrase-protected blob of
                            profiles to move to another machine
  am import [--file f] [--activate tool=name]
                            read a blob (stdin or -f) and add profiles that
                            aren't present yet; existing ones are kept as-is

tools: claude, codex, gemini   (codex/gemini run directly, no proxy)
profiles are encrypted with a master key held in the macOS Keychain.
`)
}

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		usage()
		return
	}
	// Shortcut: `am claude [args]` == `am up claude [args]` (start proxy if
	// needed, then exec). Any bare tool name works.
	if isToolName(args[0]) {
		cmdUp(args[0], args[1:])
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
	case "proxy":
		cmdProxy(args[1:])
	case "run":
		need(args, 2)
		cmdRun(args[1], args[2:])
	case "up":
		need(args, 2)
		cmdUp(args[1], args[2:])
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

func isToolName(s string) bool {
	_, ok := loadConfig().Tools[s]
	return ok
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
