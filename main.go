// Command am (project ai-cli-accounts) snapshots and swaps the local login state
// of AI CLIs (Claude Code, Codex, Gemini), and can run a local proxy that
// rotates Claude accounts automatically before a rate limit interrupts work.
package main

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
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
  am setup                  do it all: hook install + /am:feedback slash
                            command, installed globally (any project)
  am add                    save the account you're logged into; run again
                            after logging into each other account you want

Then use 'claude' normally. The proxy starts with your first session, swaps
account before a rate limit stops you (no restart), and stops when the last
session ends.

  am add [tool] [name]      save an account into a profile   (default tool:
                            claude, default name: the account email)
  am ls [tool]              list profiles with their IDs (claude1, claude2, …)
  am rm <id|name>           move a profile to the trash (asks first)
  am rename <id|name> <new> give a profile a shorter/custom name (e.g. "work")
  am restore <id|name>      bring a trashed profile back
  am restore --backup       re-import the latest auto-backup (after every add)
  am sw                     pick an account from a menu (↑/↓, Enter)
  am sw <id|name>           switch straight to it — no restart  (e.g. am sw claude2)
  am status                 what's active, rate limits, switch count
  am usage [day|week|month|all]   tokens used, by account and by model

  am hook install|uninstall|status
  am proxy                  run the proxy in the foreground (normally automatic)

  am export [tool] [name..] [-o file|--stdout]   encrypted blob (default: timestamped file)
  am import [-f file] [--activate tool=name]

  am feedback [-b|--bug|-i|--idea] [title]   file a GitHub issue (bug or idea)

<id|name> is a profile ID (claude1), an exact name, or a unique part of the
name / email. tools: claude (auto-rotated), codex, gemini (switch writes to
disk; restart the tool). Profiles are encrypted with a key in the Keychain.
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
		tool, name := toolAndName(args[1:])
		cmdAdd(tool, name)
	case "ls", "list":
		cmdLs(args[1:])
	case "rm", "remove":
		tool, name := toolAndName(args[1:])
		if name == "" {
			die("usage: am rm [tool] <name>   (name, ID, or part of the email)")
		}
		cmdRm(tool, resolveName(tool, name))
	case "rename", "mv":
		if len(args) < 3 {
			die("usage: am rename [tool] <id|name> <new-name>")
		}
		tool, name := toolAndName(args[1 : len(args)-1])
		newName := args[len(args)-1]
		if name == "" {
			die("usage: am rename [tool] <id|name> <new-name>")
		}
		cmdRename(tool, resolveName(tool, name), newName)
	case "switch", "sw":
		tool, name := toolAndName(args[1:])
		if name == "" {
			name = pickProfile(tool)
			if name == "" {
				return // cancelled
			}
		} else {
			name = resolveName(tool, name)
		}
		cmdSwitch(tool, name)
	case "restore":
		if len(args) > 1 && (args[1] == "--backup" || args[1] == "-b") {
			cmdRestoreBackup(strings.Join(args[2:], " "))
			return
		}
		tool, name := toolAndName(args[1:])
		cmdRestore(tool, name)
	case "status", "st":
		cmdStatus()
	case "hook":
		cmdHook(args[1:])
	case "proxy":
		cmdProxy(args[1:])
	case "env":
		cmdEnv(args[1:])
	case "usage":
		cmdUsage(args[1:])
	case "export":
		cmdExport(args[1:])
	case "import":
		cmdImport(args[1:])
	case "feedback":
		cmdFeedback(args[1:])
	case "setup":
		cmdSetup(args[1:])
	case "-h", "--help", "help":
		usage()
	default:
		die("unknown command %q (try: am help)", args[0])
	}
}

// toolAndName parses "[tool] [name]". The tool comes from a leading known-tool
// word ("codex foo"), or from an ID-style name ("codex1"). Otherwise the tool
// is claude and everything is the name.
func toolAndName(rest []string) (tool, name string) {
	tools := loadConfig().Tools
	if len(rest) >= 1 {
		if _, ok := tools[rest[0]]; ok {
			return rest[0], strings.Join(rest[1:], " ")
		}
	}
	joined := strings.Join(rest, " ")
	for t := range tools {
		if m := idRe(t).FindStringSubmatch(joined); m != nil {
			return t, joined
		}
	}
	return "claude", joined
}

func idRe(tool string) *regexp.Regexp {
	return regexp.MustCompile(`^` + regexp.QuoteMeta(tool) + `\d+$`)
}

func need(args []string, n int) {
	if len(args) < n {
		die("not enough arguments (try: am help)")
	}
}


// resolveName maps what the user typed to a profile name. Matches, in order:
// the short ID ("claude1"), an exact name, then a unique case-insensitive
// substring of the name or the account email. Ambiguous / missing -> error.
func resolveName(tool, q string) string {
	profs := listProfiles(tool)
	for _, p := range profs {
		if strings.EqualFold(p.ID, q) || p.Name == q {
			return p.Name
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
	fmt.Fprintln(w, "ID\tNAME\tACCOUNT\tACTIVE\tSAVED")
	for _, tn := range tools {
		active := readActivePointer(tn)
		profs := listProfiles(tn)
		if len(profs) == 0 {
			fmt.Fprintf(w, "%s\t(none — am add %s)\t\t\t\n", tn, tn)
			continue
		}
		for _, p := range profs {
			mark := ""
			if p.Name == active {
				mark = "*"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", p.ID, p.Name, orDash(p.Account), mark, p.Saved.Format("2006-01-02 15:04"))
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
// name, if given, is used as the profile name instead of the account email
// (e.g. `am add work` or `am add codex work`).
func cmdAdd(tool, name string) {
	if acct := detectAccount(toolSpec(tool)); acct != "" && profileNameForAccount(tool, acct) == "" {
		fmt.Printf("%s is logged in as %s — saving that.\n", tool, acct)
		cmdSave(tool, profileName(name, acct))
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
	cmdSave(tool, profileName(name, acct))
}

// profileName picks the name to save a profile under: the caller-given name
// if any, else the account email, sanitized either way.
func profileName(name, acct string) string {
	if name != "" {
		return sanitizeName(name)
	}
	return sanitizeName(acct)
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
