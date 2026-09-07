// Command am (module acctmgr) snapshots and swaps the local login state
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

  am ls [tool]              list saved profiles (and which is active)
  am now [tool]             show the account each tool is currently logged in as
  am save <tool> <name>     snapshot the tool's current login into a profile
  am use  <tool> <name>     restore a profile (auto-saves current state first)
  am rm   <tool> <name>     delete a profile
  am add  <tool> <name>     alias for: log in fresh, then 'am save'
  am proxy [--addr host:port]
                            run the rotating proxy (Claude: auto-switch profile
                            before the rate limit is hit, refresh OAuth tokens)
  am run  <tool> [args...]  exec the tool with env pointed at a running proxy

tools: claude, codex, gemini
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
	case "now", "current":
		cmdNow(args[1:])
	case "save":
		need(args, 3)
		cmdSave(args[1], args[2])
	case "use", "switch":
		need(args, 3)
		cmdUse(args[1], args[2])
	case "rm", "delete":
		need(args, 3)
		cmdRm(args[1], args[2])
	case "add":
		need(args, 3)
		cmdAdd(args[1], args[2])
	case "proxy":
		cmdProxy(args[1:])
	case "run":
		need(args, 2)
		cmdRun(args[1], args[2:])
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
