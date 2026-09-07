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
  am add                    save the account you're logged into; run again
                            after logging into each other account you want
  am hook install           make Claude Code start/stop the proxy, and add
                            ANTHROPIC_BASE_URL to your shell rc

Then use 'claude' normally. The proxy starts with your first session, swaps
account before a rate limit stops you (no restart), and stops when the last
session ends.

  am add [tool]             save an account into a profile   (default: claude)
  am ls [tool]              list saved profiles
  am rm [tool] <name>       delete a profile
  am switch [tool] <name>   use another account now — no restart
  am status                 what's active, rate limits, switch count

  am hook install|uninstall|status
  am proxy                  run the proxy in the foreground (normally automatic)

  am export [tool] [name..] encrypted blob of profiles for another machine
  am import [--file f] [--activate tool=name]

<name> matches an exact profile name or a unique part of it / the email.
tools: claude (auto-rotated), codex, gemini (switch writes to disk; restart
the tool). Profiles are encrypted with a key in the macOS Keychain.
`)
}

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		usage()
		return
	}
	switch args[0] {
	case "a", "add":
		cmdAdd(toolArg(args, 1))
	case "ls", "list":
		cmdLs(args[1:])
	case "rm", "remove":
		need(args, 2)
		tool, name := toolAndName(args[1:])
		cmdRm(tool, resolveName(tool, name))
	case "switch", "sw":
		need(args, 2)
		tool, name := toolAndName(args[1:])
		cmdSwitch(tool, resolveName(tool, name))
	case "status", "st":
		cmdStatus()
	case "hook":
		cmdHook(args[1:])
	case "proxy":
		cmdProxy(args[1:])
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

// toolArg returns args[i] if it names a known tool, else "claude".
func toolArg(args []string, i int) string {
	if i < len(args) {
		if _, ok := loadConfig().Tools[args[i]]; ok {
			return args[i]
		}
	}
	return "claude"
}

// toolAndName parses "[tool] <name>": if the first arg is a known tool the rest
// is the name, otherwise the tool defaults to claude and all of it is the name.
func toolAndName(rest []string) (tool, name string) {
	if len(rest) >= 2 {
		if _, ok := loadConfig().Tools[rest[0]]; ok {
			return rest[0], strings.Join(rest[1:], " ")
		}
	}
	return "claude", strings.Join(rest, " ")
}

func need(args []string, n int) {
	if len(args) < n {
		die("not enough arguments (try: am help)")
	}
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

// printLiveLogins shows the account each tool is currently logged in as (read
// straight from its credential on disk). Used by `am status` when the proxy
// isn't running.
func printLiveLogins() {
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "TOOL\tLOGGED IN AS\tSAVED PROFILE")
	for _, tn := range toolNames(loadConfig()) {
		acct := detectAccount(toolSpec(tn))
		fmt.Fprintf(w, "%s\t%s\t%s\n", tn, orDash(acct), orDash(matchProfileByAccount(tn, acct)))
	}
	w.Flush()
}

// cmdAdd captures an account into a profile. If that account is already the one
// logged in, it snapshots it straight away; otherwise it walks you through
// logging into the new account first (the CLIs need a browser for that).
func cmdAdd(tool string) {
	if acct := detectAccount(toolSpec(tool)); acct != "" && profileNameForAccount(tool, acct) == "" {
		fmt.Printf("%s is logged in as %s — saving that.\n", tool, acct)
		cmdSave(tool, sanitizeName(acct))
		fmt.Printf("\nto add a different account: log into it in %s, then run `am add %s` again.\n", tool, tool)
		return
	}
	loginHint(tool)
	fmt.Print("press Enter when you've logged in as the new account… ")
	fmt.Scanln()
	acct := detectAccount(toolSpec(tool))
	if acct == "" {
		die("still can't detect a %s login", tool)
	}
	if existing := profileNameForAccount(tool, acct); existing != "" {
		fmt.Printf("%s is already saved as %q — nothing to do.\n", acct, existing)
		return
	}
	cmdSave(tool, sanitizeName(acct))
}

func loginHint(tool string) {
	switch tool {
	case "claude":
		fmt.Println("in another terminal: `claude` → /login → sign in as the new account")
	case "codex":
		fmt.Println("in another terminal: `codex login` (after `codex logout` if needed)")
	case "gemini":
		fmt.Println("in another terminal: `gemini` → /auth → sign in as the new account")
	default:
		fmt.Printf("log into %s as the new account\n", tool)
	}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
