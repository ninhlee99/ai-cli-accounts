package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"text/tabwriter"

	"ai-cli-accounts/pkg/env"
	"ai-cli-accounts/pkg/hook"
	"ai-cli-accounts/pkg/profile"
	"ai-cli-accounts/pkg/provider"
	"ai-cli-accounts/pkg/proxy"
	"ai-cli-accounts/pkg/ui"
	"ai-cli-accounts/pkg/usage"
)

const feedbackRepo = "ninhlee99/ai-cli-accounts"

func die(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "am: "+format+"\n", a...)
	os.Exit(1)
}

func usageHelp() {
	fmt.Print(`am - AI CLI account manager & Local AI Gateway

Setup (once):
  am setup                  do it all: hook install + /am:feedback slash command

Account Profiles:
  am add [tool] [name]      save active account into a profile (default: claude)
  am ls [tool]              list saved profiles with short IDs (claude1, ...)
  am rm <id|name>           move a profile to trash (asks first)
  am rename <id|name> <new> rename a profile
  am restore <id|name>      restore a trashed profile
  am restore --backup       re-import latest auto-backup
  am sw                     interactive account & provider picker (↑/↓, Enter)
  am sw <id|name|provider>  switch to a specific account or LLM provider
  am current [tool]         print the currently active account on system
  am status                 proxy state, rate limits (5h/7d), provider pool

Multi-Provider Gateway & Plugins:
  am login [provider]       login chatgpt, claude, gemini, github, groq
  am accounts               list multi-provider accounts in pool
  am api add <name> --endpoint <url> --api-key <key> [--model M] [--priority N]
                            add OpenAI-compatible provider
  am api rm <name>          remove provider
  am api ls                 list provider accounts
  am chat [prompt]          interactive terminal chat via multi-provider pool

Monitoring & Utilities:
  am usage [day|week|month|all] [-D|--detail] [-d YYYY-MM-DD] [-p PROJECT]
                            token usage analytics
  am run <tool> [args...]   exec tool (currently: claude) routed through the proxy
  am proxy [up|down]        run or manage background proxy daemon (default :8787)
  am env                    print export ANTHROPIC_BASE_URL=... for eval "$(am env)"
  am hook [install|uninstall|status]
  am export [tool] [name..] [-o file|--stdout]  encrypted profile bundle
  am import [-f file] [--activate tool=name]
  am feedback               file a GitHub issue for bugs or ideas
`)
}

// Run executes the am CLI command with the given argument list (including program name as args[0]).
func Run(rawArgs []string) {
	if len(rawArgs) < 2 {
		usageHelp()
		return
	}

	cmd := rawArgs[1]
	args := rawArgs[2:]

	switch cmd {
	case "help", "-h", "--help":
		usageHelp()

	case "setup":
		cmdSetup()

	case "a", "add":
		tool, name := toolAndName(args)
		cmdAdd(tool, name)

	case "ls", "list":
		cmdLs(args)

	case "rm", "remove", "delete":
		tool, name := toolAndName(args)
		if name == "" {
			die("usage: am rm [tool] <name>   (name, ID, or part of the email)")
		}
		cmdRm(tool, resolveName(tool, name))

	case "rename", "mv":
		if len(args) < 2 {
			die("usage: am rename [tool] <id|name> <new-name>")
		}
		tool, name := toolAndName(args[:len(args)-1])
		newName := args[len(args)-1]
		if name == "" {
			die("usage: am rename [tool] <id|name> <new-name>")
		}
		cmdRename(tool, resolveName(tool, name), newName)

	case "restore":
		if len(args) > 0 && (args[0] == "--backup" || args[0] == "-b") {
			profile.CmdRestoreBackup(strings.Join(args[1:], " "))
			return
		}
		tool, name := toolAndName(args)
		if name == "" {
			die("usage: am restore [tool] <id|name> | am restore --backup")
		}
		if err := profile.RestoreProfile(tool, name); err != nil {
			die("%v", err)
		}

	case "sw", "switch":
		tool, name := toolAndName(args)
		if name == "" {
			name = ui.PickProfile(tool)
			if name == "" {
				return
			}
		}
		if tool == "claude" && isProviderName(name) && !hasProfile(tool, name) {
			proxy.CmdSwitchProvider(name)
		} else {
			resolved := resolveName(tool, name)
			proxy.CmdSwitch(tool, resolved)
		}

	case "status", "st":
		ui.CmdStatus()

	case "current":
		tool := "claude"
		if len(args) > 0 {
			tool = args[0]
		}
		spec, ok := profile.LookupToolSpec(tool)
		if !ok {
			die("unknown tool %q", tool)
		}
		acct := profile.DetectAccount(spec)
		if acct == "" {
			fmt.Println("not logged in")
			return
		}
		fmt.Printf("%s logged in: %s\n", tool, acct)

	case "login":
		ui.CmdLogin(args)

	case "accounts":
		ui.CmdAccounts()

	case "api":
		ui.CmdAPI(args)

	case "chat":
		ui.CmdChat(args)

	case "usage":
		usage.PrintUsageReport(args)

	case "run":
		cmdRun(args)

	case "env":
		if len(args) == 0 {
			env.PrintEnvExports(proxy.ProxyUp(), len(profile.ListProfiles("claude")) > 0, proxy.ProxyBase())
			return
		}
		switch args[0] {
		case "set":
			if len(args) < 3 {
				die("am env set KEY VALUE")
			}
			m := env.LoadEnvVars()
			m[args[1]] = args[2]
			_ = env.SaveEnvVars(m)
			fmt.Fprintf(os.Stderr, "set %s\n", args[1])
		case "get":
			if len(args) < 2 {
				die("am env get KEY")
			}
			v, ok := env.LoadEnvVars()[args[1]]
			if !ok {
				die("%s not set", args[1])
			}
			fmt.Println(v)
		case "rm", "unset":
			if len(args) < 2 {
				die("am env rm KEY")
			}
			m := env.LoadEnvVars()
			delete(m, args[1])
			_ = env.SaveEnvVars(m)
			fmt.Fprintf(os.Stderr, "removed %s\n", args[1])
		case "list", "ls":
			m := env.LoadEnvVars()
			for k, v := range m {
				fmt.Printf("%s=%s\n", k, v)
			}
		default:
			die("am env: [set KEY VALUE | get KEY | rm KEY | list]")
		}

	case "hook":
		sub := "status"
		if len(args) > 0 {
			sub = args[0]
		}
		switch sub {
		case "install":
			cmdHookInstall()
		case "uninstall", "remove":
			n, err := hook.HookUninstall()
			if err != nil {
				die("hook uninstall: %v", err)
			}
			fmt.Printf("removed %d hook entr%s from %s\n", n, plural(n, "y", "ies"), hook.ClaudeSettingsPath())
			fmt.Println("also remove the `eval \"$(am env)\"` line from your shell rc if you added it.")
		case "status":
			cmdHookStatus()
		default:
			die("am hook: install | uninstall | status")
		}

	case "proxy":
		if len(args) > 0 {
			switch args[0] {
			case "up":
				proxy.CmdProxyUp()
				return
			case "down":
				force := false
				yes := false
				for _, a := range args[1:] {
					if a == "--force" {
						force = true
					}
					if a == "--yes-i-know" {
						yes = true
					}
				}
				proxy.CmdProxyDown(force, yes)
				return
			}
		}
		addr := "127.0.0.1:8787"
		upstream := "https://api.anthropic.com"
		for i := 0; i < len(args); i++ {
			switch args[i] {
			case "--addr":
				if i+1 < len(args) {
					addr = args[i+1]
					i++
				}
			case "--upstream":
				if i+1 < len(args) {
					upstream = args[i+1]
					i++
				}
			}
		}
		if err := proxy.RunProxy(addr, upstream); err != nil {
			die("proxy error: %v", err)
		}

	case "export":
		if err := profile.CmdExport(args); err != nil {
			die("%v", err)
		}

	case "import":
		if err := profile.CmdImport(args); err != nil {
			die("%v", err)
		}


	case "feedback":
		cmdFeedback(args)

	default:
		die("unknown command %q — run 'am help' for usage", cmd)
	}
}

func toolAndName(rest []string) (tool, name string) {
	tools := profile.LoadConfig().Tools
	if len(rest) >= 1 {
		if _, ok := tools[rest[0]]; ok {
			return rest[0], strings.Join(rest[1:], " ")
		}
	}
	joined := strings.Join(rest, " ")
	for t := range tools {
		if regexp.MustCompile(`^` + regexp.QuoteMeta(t) + `\d+$`).MatchString(joined) {
			return t, joined
		}
	}
	return "claude", joined
}

func resolveName(tool, q string) string {
	profs := profile.ListProfiles(tool)
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

func hasProfile(tool, name string) bool {
	for _, p := range profile.ListProfiles(tool) {
		if strings.EqualFold(p.ID, name) || strings.EqualFold(p.Name, name) {
			return true
		}
	}
	return false
}

func isProviderName(name string) bool {
	f, err := provider.LoadConfigFile(provider.DefaultAccountsPath())
	if err != nil || f == nil {
		return false
	}
	for _, p := range f.Providers {
		if strings.EqualFold(p.ID, name) {
			return true
		}
	}
	return false
}

func cmdLs(args []string) {
	c := profile.LoadConfig()
	tools := profile.ToolNames(c)
	if len(args) > 0 {
		tools = []string{args[0]}
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tACCOUNT\tACTIVE\tSAVED")
	for _, tn := range tools {
		active := profile.ReadActivePointer(tn)
		profs := profile.ListProfiles(tn)
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

func cmdAdd(tool, name string) {
	spec, ok := profile.LookupToolSpec(tool)
	if !ok {
		die("unknown tool %q", tool)
	}
	if acct := profile.DetectAccount(spec); acct != "" && profile.ProfileNameForAccount(tool, acct) == "" {
		fmt.Printf("%s is logged in as %s — saving that.\n", tool, acct)
		pName := profileName(name, acct)
		if _, err := profile.CmdSave(tool, pName); err != nil {
			die("save failed: %v", err)
		}
		fmt.Printf("\nto add a different account: log into it in %s, then run `am add %s` again.\n", tool, tool)
		return
	}
	loginHint(tool)
	fmt.Print("press Enter when you've logged in as the new account… ")
	var ignored string
	_, _ = fmt.Scanln(&ignored)
	acct := profile.DetectAccount(spec)
	if acct == "" {
		die("still can't detect a %s login", tool)
	}
	if existing := profile.ProfileNameForAccount(tool, acct); existing != "" {
		fmt.Printf("%s is already saved as %q — nothing to do.\n", acct, existing)
		return
	}
	pName := profileName(name, acct)
	if _, err := profile.CmdSave(tool, pName); err != nil {
		die("save failed: %v", err)
	}
}

func profileName(name, acct string) string {
	if name != "" {
		return profile.SanitizeName(name)
	}
	return profile.SanitizeName(acct)
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

func cmdRm(tool, name string) {
	if _, err := os.Stat(profile.BundlePath(tool, name)); err != nil {
		die("no profile %s/%s", tool, name)
	}
	m := profile.ReadMeta(tool, name)
	if !confirm(fmt.Sprintf("delete %s (%s)?", name, orDash(m.Account))) {
		fmt.Println("kept.")
		return
	}
	profile.AutoBackup()
	if err := profile.TrashProfile(tool, name); err != nil {
		die("remove failed: %v", err)
	}
	fmt.Printf("moved to trash — restore with: am restore %s\n", name)
}

func confirm(prompt string) bool {
	if os.Getenv("AM_YES") != "" {
		return true
	}
	fmt.Printf("%s [y/N] ", prompt)
	var ans string
	_, _ = fmt.Scanln(&ans)
	return strings.EqualFold(strings.TrimSpace(ans), "y")
}

func cmdRename(tool, name, newName string) {
	if _, err := os.Stat(profile.BundlePath(tool, name)); err != nil {
		die("no profile %s/%s (see: am ls %s)", tool, name, tool)
	}
	newName = profile.SanitizeName(newName)
	if newName == "" {
		die("new name can't be empty")
	}
	if newName == name {
		fmt.Println("already named that.")
		return
	}
	if _, err := os.Stat(profile.BundlePath(tool, newName)); err == nil {
		die("%s/%s already exists", tool, newName)
	}
	if err := os.Rename(profile.BundlePath(tool, name), profile.BundlePath(tool, newName)); err != nil {
		die("rename: %v", err)
	}
	m := profile.ReadMeta(tool, name)
	m.Name = newName
	mb, _ := json.MarshalIndent(m, "", "  ")
	_ = os.Remove(profile.MetaPath(tool, name))
	if err := profile.WriteFileAtomic(profile.MetaPath(tool, newName), mb, 0o600); err != nil {
		die("write meta: %v", err)
	}
	if profile.ReadActivePointer(tool) == name {
		profile.WriteActivePointer(tool, newName)
	}
	fmt.Printf("renamed %s/%s -> %s\n", tool, name, newName)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// cmdRun execs tool in-place (replacing this process) with env vars pointed
// at the local rotating proxy, so the tool's own requests get account
// rotation for free instead of talking to the upstream API directly.
func cmdRun(args []string) {
	if len(args) < 1 {
		die("am run <tool> [args...]")
	}
	tool := args[0]
	rest := args[1:]
	if tool != "claude" {
		die("am run currently supports: claude")
	}
	base := proxy.ProxyBase()
	if !proxy.ProxyUp() {
		die("proxy not reachable at %s (start it with: am proxy)", base)
	}
	environ := append(os.Environ(),
		"ANTHROPIC_BASE_URL="+base,
		"ANTHROPIC_AUTH_TOKEN=am-proxy",
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
	)
	bin, err := exec.LookPath(tool)
	if err != nil {
		die("%v", err)
	}
	_ = syscall.Exec(bin, append([]string{tool}, rest...), environ)
}


func cmdSetup() {
	fmt.Println("== Setting up Claude Code hook ==")
	cmdHookInstall()

	fmt.Println("\n== Installing /am:feedback slash command ==")
	if err := hook.InstallSlashCommand("feedback.md", []byte(hook.FeedbackSlashCommandContent)); err != nil {
		fmt.Printf("Slash command error: %v\n", err)
	}

	fmt.Println("\nsetup done. Open a new shell, then: am add   (save your first account)")
}

func cmdHookInstall() {
	if err := hook.HookInstall(); err != nil {
		die("hook install: %v", err)
	}
	fmt.Printf("installed hooks in %s\n  SessionStart -> am proxy up\n  SessionEnd   -> am proxy down\n  Stop         -> am proxy down\n\n", hook.ClaudeSettingsPath())

	line := `eval "$(am env)"`
	rc := hook.ShellRC()
	if rc == "" {
		fmt.Printf("add this to your shell rc (once), then open a new shell:\n  %s\n", line)
		return
	}
	if hook.RCHasLine(rc, line) {
		fmt.Println("shell rc already wired to `am env` — done.")
		return
	}
	fmt.Printf("add this to %s (once):\n  %s\n", rc, line)
	fmt.Print("append it now? [y/N] ")
	r := bufio.NewReader(os.Stdin)
	ans, _ := r.ReadString('\n')
	if strings.EqualFold(strings.TrimSpace(ans), "y") {
		if err := hook.AppendLine(rc, "\n# ai-cli-accounts: route claude through the rotating proxy\n"+line+"\n"); err != nil {
			fmt.Printf("append failed: %v\n", err)
			return
		}
		fmt.Printf("appended. open a new shell (or `source %s`).\n", rc)
		return
	}
	fmt.Println("then open a new shell.")
}

func cmdHookStatus() {
	events := hook.InstalledEvents()
	if len(events) == 0 {
		fmt.Println("hooks: not installed  (am hook install)")
	} else {
		for _, ev := range events {
			fmt.Printf("hook: %s -> am proxy\n", ev)
		}
	}
	switch os.Getenv("ANTHROPIC_BASE_URL") {
	case proxy.ProxyBase():
		fmt.Printf("ANTHROPIC_BASE_URL: %s\n", proxy.ProxyBase())
	case "":
		fmt.Println("ANTHROPIC_BASE_URL: not set — claude will bypass the proxy (run `am hook install` to wire `am env`)")
	default:
		fmt.Printf("ANTHROPIC_BASE_URL: %s  (not the proxy)\n", os.Getenv("ANTHROPIC_BASE_URL"))
	}
	if proxy.ProxyUp() {
		fmt.Println("proxy: running")
	} else {
		fmt.Println("proxy: not running (normal when no claude session is open)")
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func cmdFeedback(args []string) {
	kind := "bug"
	var titleWords []string
	for _, a := range args {
		switch a {
		case "-b", "--bug":
			kind = "bug"
		case "-i", "--idea":
			kind = "idea"
		default:
			titleWords = append(titleWords, a)
		}
	}
	title := strings.Join(titleWords, " ")
	if title == "" {
		fmt.Print("short title for the issue: ")
		r := bufio.NewReader(os.Stdin)
		line, _ := r.ReadString('\n')
		title = strings.TrimSpace(line)
		if title == "" {
			die("cancelled (no title given)")
		}
	}

	fmt.Println("describe what happened / what you'd like — blank line to finish:")
	sc := bufio.NewScanner(os.Stdin)
	var lines []string
	for sc.Scan() {
		l := sc.Text()
		if strings.TrimSpace(l) == "" {
			break
		}
		lines = append(lines, l)
	}
	body := strings.Join(lines, "\n")

	var b strings.Builder
	if body != "" {
		b.WriteString(body)
		b.WriteString("\n\n")
	}
	b.WriteString("---\n")
	fmt.Fprintf(&b, "OS: %s/%s\n", runtime.GOOS, runtime.GOARCH)
	if active := profile.ReadActivePointer("claude"); active != "" {
		fmt.Fprintf(&b, "active claude profile: %s\n", active)
	}

	label := "bug"
	if kind == "idea" {
		label = "enhancement"
	}

	ghPath, err := exec.LookPath("gh")
	if err == nil {
		cmd := exec.Command(ghPath, "issue", "create", "-R", feedbackRepo, "-t", title, "-b", b.String(), "-l", label)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Stdin = os.Stdin
		if err := cmd.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "am: gh issue create failed (%v) — opening browser instead\n", err)
		} else {
			return
		}
	}

	u := fmt.Sprintf("https://github.com/%s/issues/new?title=%s&body=%s",
		feedbackRepo, url.QueryEscape(title), url.QueryEscape(b.String()))
	fmt.Println("opening:", u)
	var openCmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		openCmd = exec.Command("open", u)
	case "linux":
		openCmd = exec.Command("xdg-open", u)
	default:
		fmt.Println("open that URL in a browser to file the issue.")
		return
	}
	if err := openCmd.Start(); err != nil {
		fmt.Println("couldn't launch a browser — open the URL above manually.")
	}
}
