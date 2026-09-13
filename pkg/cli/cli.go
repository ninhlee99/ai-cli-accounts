package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"amux-accounts/pkg/env"
	"amux-accounts/pkg/hook"
	"amux-accounts/pkg/monitor"
	"amux-accounts/pkg/privacy"
	"amux-accounts/pkg/profile"
	"amux-accounts/pkg/provider"
	"amux-accounts/pkg/proxy"
	"amux-accounts/pkg/types"
	"amux-accounts/pkg/ui"
	"amux-accounts/pkg/usage"
)

const feedbackRepo = "ninhlee99/amux"

func die(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "amux: "+format+"\n", a...)
	os.Exit(1)
}

func usageHelp() {
	fmt.Print(`amux - AI CLI account manager & Local AI Gateway

Setup (once):
  amux setup [--auto-update]  hook install + /am:feedback + auto-update

Accounts:
  amux accounts               list ALL accounts (Claude + web + API)
  amux off <id>               take any account out of rotate (stays in list)
  amux on <id>                put it back
  amux add [tool] [name]      save current CLI login (claude / codex / gemini)
  amux rm <id|name>           delete account (CLI profile → trash, provider → removed)
  amux rename <id> <new>      rename Claude profile
  amux restore <id>           restore trash (` + "`am restore --backup`" + ` = last auto-backup)
  amux sw                     picker · am sw <id> pin Claude or provider
  amux current [tool]         who is logged in on this machine
  amux ls [tool]              alias to amux accounts

Rotate pool:
  amux pool                   who is IN rotate
  amux pool set <id> [flags]  set account options (--priority N, --model M, --on, --off)
  amux pool add <id>          include in rotate (same as am on)
  amux pool remove <id>       exclude from rotate (same as am off)
  amux pool priority <id> N   lower N = tried first (hot-reload)
  amux pool model <id> M      change provider model (hot-reload)

Add providers:
  amux login <provider>       chatgpt / claude / gemini / gemini-web / github / groq
  amux api add <name> --endpoint <url> --api-key <key> [--model M] [--priority N]
  amux accounts rm <id>       delete a provider from the list (or use am rm <id>)
  amux doctor providers       1-turn probe each adapter
  amux chat [--provider id]   terminal chat + failover

  Codex: after am add codex, token is reused as codex:NN — no extra login.

Monitoring & Utilities:
  amux update [--force] [--quiet]
                            update amux to latest version from github (keeps all accounts)
  amux usage [day|week|month|all] [-D|--detail] [-d YYYY-MM-DD] [-p PROJECT]
                            token usage analytics
  amux logs [--count] [--errors] [--clean]
                            log statistics, errors, and 7-day retention cleanup
  amux run <tool> [args...]   exec tool (claude / agy / antigravity) routed through the proxy
  amux proxy [up|down|token] [--public] [-b|--addr HOST] [-p|--port N] [--threshold N]
                            run/manage proxy daemon (default 127.0.0.1:8787;
                            --public binds 0.0.0.0; -p/--port overrides port)
                            --public requires an admin token for non-loopback
                            requests — 'amux proxy token' prints/generates it
                            --threshold N  auto-switch Claude account when 5h/7d
                            utilization >= N% (default 95). Also: AM_ROTATE_THRESHOLD
  amux env [--public]         print export ANTHROPIC_BASE_URL=... for eval "$(amux env)";
                            --public uses LAN IP when proxy is bound on 0.0.0.0
  amux hook [install|uninstall|status]
  amux export [tool] [name..] [-o file|--stdout]  encrypted profile bundle
  amux import [-f file] [--activate tool=name]
  amux feedback [--error]     file a GitHub issue for bugs or errors (privacy sanitized)
`)
}

// Run executes the am CLI command with the given argument list (including program name as args[0]).
func Run(rawArgs []string) {
	if len(rawArgs) < 2 {
		usageHelp()
		return
	}

	monitor.EnableTermSink()

	// One-time (cheap-after-first-run) migration of the old fixed-literal
	// pool provider IDs (claude-web, chatgpt-web, ...) to the unified
	// "<prefix>:<NN>" format. Runs before dispatch so every subcommand sees
	// already-migrated IDs.
	if err := provider.MigrateLegacyIDs(provider.DefaultAccountsPath()); err != nil {
		fmt.Fprintf(os.Stderr, "amux: warning: could not migrate account IDs: %v\n", err)
	} else {
		// Reload in-memory pool if proxy already up (IDs may have changed).
		proxy.Sync()
	}
	if removed, err := provider.DeduplicateProvidersByCredential(provider.DefaultAccountsPath()); err != nil {
		fmt.Fprintf(os.Stderr, "amux: warning: could not dedupe credentials: %v\n", err)
	} else if len(removed) > 0 {
		fmt.Fprintf(os.Stderr, "amux: removed duplicate credential providers: %s\n", strings.Join(removed, ", "))
	}

	cmd := rawArgs[1]
	args := rawArgs[2:]

	switch cmd {
	case "help", "-h", "--help":
		usageHelp()

	case "setup":
		cmdSetup(args)

	case "update", "upgrade":
		force := false
		quiet := false
		for _, a := range args {
			if a == "--force" || a == "-f" {
				force = true
			}
			if a == "--quiet" || a == "-q" {
				quiet = true
			}
		}
		cmdUpdate(force, quiet)

	case "a", "add":
		tool, name := toolAndName(args)
		cmdAdd(tool, name)

	case "ls", "list":
		cmdLs(args)

	case "rm", "remove", "delete":
		target := strings.Join(args, " ")
		if target == "" {
			die("usage: amux rm [tool] <name>   (account ID, name, or provider ID)")
		}
		if id, err := provider.MatchID(provider.DefaultAccountsPath(), target); err == nil {
			cmdRm("", id)
			return
		}
		tool, name := toolAndName(args)
		if name == "" {
			name = tool
			tool = "claude"
		}
		if id, err := provider.MatchID(provider.DefaultAccountsPath(), name); err == nil {
			cmdRm("", id)
			return
		}
		cmdRm(tool, resolveName(tool, name))

	case "rename", "mv":
		if len(args) < 2 {
			die("usage: amux rename [tool] <id|name> <new-name>")
		}
		tool, name := toolAndName(args[:len(args)-1])
		newName := args[len(args)-1]
		if name == "" {
			die("usage: amux rename [tool] <id|name> <new-name>")
		}
		cmdRename(tool, resolveName(tool, name), newName)

	case "restore":
		if len(args) > 0 && (args[0] == "--backup" || args[0] == "-b") {
			profile.CmdRestoreBackup(strings.Join(args[1:], " "))
			return
		}
		tool, name := toolAndName(args)
		if name == "" {
			die("usage: amux restore [tool] <id|name> | amux restore --backup")
		}
		if err := profile.RestoreProfile(tool, name); err != nil {
			die("%v", err)
		}

	case "sw", "switch":
		if len(args) > 0 && isProviderName(strings.Join(args, " ")) {
			proxy.CmdSwitchProvider(strings.Join(args, " "))
			break
		}
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
			if profile.IsDisabled(tool, resolved) {
				die("profile %q is off — run: am on %s", resolved, resolved)
			}
			proxy.CmdSwitch(tool, resolved)
		}

	case "off", "disable":
		if len(args) == 0 {
			die("usage: am off <id>")
		}
		if len(args) >= 2 {
			if _, ok := profile.LoadConfig().Tools[args[0]]; ok {
				resolved := resolveName(args[0], strings.Join(args[1:], " "))
				if err := profile.SetDisabled(args[0], resolved, true); err != nil {
					die("%v", err)
				}
				proxy.Sync()
				fmt.Printf("off %s/%s — out of rotate (am on %s)\n", args[0], resolved, resolved)
				return
			}
		}
		cmdToggleAccount(strings.Join(args, " "), false)

	case "on", "enable":
		if len(args) == 0 {
			die("usage: am on <id>")
		}
		if len(args) >= 2 {
			if _, ok := profile.LoadConfig().Tools[args[0]]; ok {
				resolved := resolveName(args[0], strings.Join(args[1:], " "))
				if err := profile.SetDisabled(args[0], resolved, false); err != nil {
					die("%v", err)
				}
				proxy.Sync()
				fmt.Printf("on %s/%s — back in rotate\n", args[0], resolved)
				return
			}
		}
		cmdToggleAccount(strings.Join(args, " "), true)

	case "pool":
		cmdPool(args)

	case "status", "st":
		ui.CmdStatus()

	case "watch", "dash", "dashboard":
		ui.CmdWatch()

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

	case "doctor":
		if len(args) > 0 && (args[0] == "providers" || args[0] == "provider") {
			ui.CmdDoctorProviders()
			return
		}
		fmt.Println("Usage: amux doctor providers")

	case "accounts":
		ui.CmdAccountsCmd(args)

	case "api":
		ui.CmdAPI(args)

	case "chat":
		ui.CmdChat(args)

	case "usage":
		usage.PrintUsageReport(args)

	case "logs", "log":
		cmdLogs(args)

	case "run":
		cmdRun(args)

	case "env":
		if len(args) == 0 || (len(args) == 1 && args[0] == "--public") {
			base := proxy.ProxyBase()
			if len(args) == 1 && args[0] == "--public" {
				if pub := proxy.PublicBaseURL(proxy.ListenAddr()); pub != "" {
					base = pub
				} else {
					die("proxy not listening publicly (start with: am proxy up --public)")
				}
			}
			env.PrintEnvExports(proxy.ProxyUp(), len(profile.ListProfiles("claude")) > 0, base)
			return
		}
		switch args[0] {
		case "set":
			if len(args) < 3 {
				die("amux env set KEY VALUE")
			}
			m := env.LoadEnvVars()
			m[args[1]] = args[2]
			_ = env.SaveEnvVars(m)
			fmt.Fprintf(os.Stderr, "set %s\n", args[1])
		case "get":
			if len(args) < 2 {
				die("amux env get KEY")
			}
			v, ok := env.LoadEnvVars()[args[1]]
			if !ok {
				die("%s not set", args[1])
			}
			fmt.Println(v)
		case "rm", "unset":
			if len(args) < 2 {
				die("amux env rm KEY")
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
			die("amux env: [set KEY VALUE | get KEY | rm KEY | list]")
		}

	case "hook":
		sub := ""
		if len(args) > 0 {
			sub = strings.ToLower(args[0])
		}
		switch sub {
		case "install":
			cmdHookInstall(args[1:])
		case "uninstall", "remove":
			cmdHookUninstall(args[1:])
		case "status":
			cmdHookStatus(args[1:])
		case "claude":
			cmdHookTool("claude", args[1:])
		case "agy", "antigravity":
			cmdHookTool("agy", args[1:])
		case "codex":
			cmdHookTool("codex", args[1:])
		case "cursor":
			cmdHookTool("cursor", args[1:])
		case "agy-start":
			cmdHookTool("agy", []string{"start"})
		case "agy-stop":
			cmdHookTool("agy", []string{"stop"})
		default:
			die("amux hook: [claude | agy | codex | cursor] | install | uninstall | status")
		}

	case "proxy":
		if len(args) > 0 {
			switch args[0] {
			case "up":
				threshold := proxyThresholdFromArgs(args[1:])
				listen := proxyListenFromArgs(args[1:])
				proxy.CmdProxyUpWithAddr(listen, threshold)
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
			case "token":
				tok, err := proxy.LoadOrCreateAuthToken()
				if err != nil {
					die("proxy token: %v", err)
				}
				fmt.Println(tok)
				return
			}
		}
		addr := proxy.ResolveListenAddr(proxyListenFromArgs(args))
		if addr == "" {
			addr = "127.0.0.1:8787"
		}
		upstream := "https://api.anthropic.com"
		supervise := false
		threshold := proxyThresholdFromArgs(args)
		for i := 0; i < len(args); i++ {
			switch args[i] {
			case "--addr", "-b", "--port", "-p", "--public":
				// consumed by proxyListenFromArgs
				if (args[i] == "--addr" || args[i] == "-b" || args[i] == "--port" || args[i] == "-p") && i+1 < len(args) {
					i++
				}
			case "--upstream":
				if i+1 < len(args) {
					upstream = args[i+1]
					i++
				}
			case "--threshold":
				// consumed by proxyThresholdFromArgs
				if i+1 < len(args) {
					i++
				}
			case "--supervise":
				// Internal: how CmdProxyUp spawns the daemon (watchdog +
				// Anthropic-direct fallback wrapper around the real
				// server, see pkg/proxy/supervisor.go). Not meant to be
				// typed by hand, but not hidden either — --addr/--upstream
				// aren't either.
				supervise = true
			}
		}
		if proxyListenFromArgs(args) != "" {
			_ = proxy.SaveListenAddr(addr)
		}
		proxy.SetUsedThreshold(threshold)
		if supervise {
			if err := proxy.RunSupervisor(addr, upstream, threshold); err != nil {
				die("proxy supervisor error: %v", err)
			}
			return
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
		die("unknown command %q — run 'amux help' for usage", cmd)
	}
}

// proxyThresholdFromArgs reads --threshold N from args, else AM_ROTATE_THRESHOLD,
// else the 95% default. Values may be percent (95) or fraction (0.95).
func proxyThresholdFromArgs(args []string) float64 {
	for i := 0; i < len(args); i++ {
		if args[i] == "--threshold" && i+1 < len(args) {
			f, err := strconv.ParseFloat(args[i+1], 64)
			if err != nil {
				die("invalid --threshold %q", args[i+1])
			}
			return proxy.ParseUsedThreshold(f)
		}
	}
	if env := os.Getenv("AM_ROTATE_THRESHOLD"); env != "" {
		if f, err := strconv.ParseFloat(env, 64); err == nil {
			return proxy.ParseUsedThreshold(f)
		}
	}
	return proxy.DefaultUsedThreshold
}

// proxyListenFromArgs returns an explicit listen override from --public,
// --addr/-b, and/or --port/-p. Empty means "use env / persisted / default".
func proxyListenFromArgs(args []string) string {
	listen, ok, err := proxy.ParseListenArgs(args)
	if err != nil {
		die("%v", err)
	}
	if !ok {
		return ""
	}
	return listen
}

func toolAndName(rest []string) (tool, name string) {
	tools := profile.LoadConfig().Tools
	if len(rest) >= 1 {
		if _, ok := tools[rest[0]]; ok {
			return rest[0], strings.Join(rest[1:], " ")
		}
	}
	joined := strings.Join(rest, " ")
	if prefix, _, ok := types.ParseID(joined); ok {
		// Legacy flat prefixes (codexcli, geminicli, ...) migrate to their
		// current unified-ID prefix (codex, antigravity, ...) before
		// matching against IDPrefixForTool, which only ever returns the
		// unified form.
		if migrated, hit := types.CompactPrefixMigrate[prefix]; hit {
			prefix = migrated
		}
		for t := range tools {
			if profile.IDPrefixForTool(t) == prefix {
				return t, joined
			}
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
	fmt.Fprintln(w, "ID\tNAME\tACCOUNT\tACTIVE\tOFF\tSAVED")
	for _, tn := range tools {
		active := profile.ReadActivePointer(tn)
		profs := profile.ListProfiles(tn)
		if len(profs) == 0 {
			fmt.Fprintf(w, "%s\t(none — am add %s)\t\t\t\t\n", tn, tn)
			continue
		}
		for _, p := range profs {
			mark := ""
			if p.Name == active {
				mark = "*"
			}
			off := ""
			if p.Disabled {
				off = "yes"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", p.ID, p.Name, orDash(p.Account), mark, off, p.Saved.Format("2006-01-02 15:04"))
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
		fmt.Printf("\nto add a different account: log into it in %s, then run `amux add %s` again.\n", tool, tool)
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
	target := name
	if target == "" {
		target = tool
	}
	// Support deleting pool providers seamlessly via am rm <id>
	if id, err := provider.MatchID(provider.DefaultAccountsPath(), target); err == nil {
		if !confirm(fmt.Sprintf("delete provider %s from accounts list?", id)) {
			fmt.Println("kept.")
			return
		}
		if err := provider.RemoveProvider(provider.DefaultAccountsPath(), id); err != nil {
			die("remove provider failed: %v", err)
		}
		proxy.Sync()
		fmt.Printf("Removed provider %q from accounts\n", id)
		return
	}

	if _, err := os.Stat(profile.BundlePath(tool, name)); err != nil {
		die("no account or provider matching %q (see: am accounts)", name)
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

func cmdLogs(args []string) {
	showErrors := false
	clean := false
	for _, a := range args {
		switch a {
		case "-e", "--error", "--errors":
			showErrors = true
		case "-c", "--clean", "--prune":
			clean = true
		}
	}

	if clean {
		removed, err := monitor.PruneLogs(7 * 24 * time.Hour)
		if err != nil {
			die("prune logs failed: %v", err)
		}
		fmt.Printf("Pruned %d log entries older than 7 days.\n", removed)
		return
	}

	if showErrors {
		errLog, found := monitor.GetLatestErrorLog()
		if !found || strings.TrimSpace(errLog) == "" {
			fmt.Println("No error logs found.")
			return
		}
		fmt.Println("=== Latest Error Log ===")
		fmt.Println(errLog)
		return
	}

	st := monitor.GetLogStats()
	lastErr := st.LastError
	if lastErr == "" {
		lastErr = "none"
	}
	if len(lastErr) > 36 {
		lastErr = lastErr[:33] + "..."
	}

	fmt.Println()
	fmt.Println("  +-- AMUX LOG STATS --------------------------------------+")
	fmt.Printf("  |total requests : %-39d|\n", st.TotalRequests)
	fmt.Printf("  |total errors   : %-39d|\n", st.TotalErrors)
	fmt.Printf("  |last error     : %-39s|\n", lastErr)
	fmt.Println("  |retention      : 7 days (auto-pruned)                   |")
	fmt.Println("  +--------------------------------------------------------+")
	fmt.Println("  am logs --errors    view latest error details")
	fmt.Println("  am logs --clean     prune logs older than 7 days")
	fmt.Println("  am feedback --error create GitHub issue from error log")
	fmt.Println()
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
		die("amux run <tool> [args...]")
	}
	tool := args[0]
	rest := args[1:]
	if !proxy.ProxyUp() {
		proxy.CmdProxyUpFlags(proxy.UpFlags{})
	}
	base := proxy.ProxyBase()

	var bin string
	var execArgs []string
	var environ []string

	switch tool {
	case "claude":
		var err error
		bin, err = exec.LookPath(tool)
		if err != nil {
			die("%v", err)
		}
		execArgs = append([]string{tool}, rest...)
		environ = append(os.Environ(),
			"ANTHROPIC_BASE_URL="+base,
			"ANTHROPIC_AUTH_TOKEN=am-proxy",
			"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
		)
	case "agy", "antigravity":
		target := tool
		var err error
		bin, err = exec.LookPath(target)
		if err != nil && tool == "antigravity" {
			target = "agy"
			bin, err = exec.LookPath(target)
		}
		if err != nil {
			die("%v", err)
		}
		execArgs = append([]string{target}, rest...)
		environ = append(os.Environ(),
			"GEMINI_API_BASE="+base,
			"GOOGLE_GENAI_BASE_URL="+base,
		)
	default:
		die("amux run currently supports: claude, agy, antigravity")
	}

	_ = syscall.Exec(bin, execArgs, environ)
}

func cmdSetup(args []string) {
	autoUpdate := false
	disableAutoUpdate := false
	for _, a := range args {
		if a == "--auto-update" || a == "-u" {
			autoUpdate = true
		}
		if a == "--no-auto-update" || a == "--disable-auto-update" {
			disableAutoUpdate = true
		}
	}

	fmt.Println("== Setting up AI CLI hooks (Claude, AGY, Codex, Cursor) ==")
	cmdHookInstall(nil)

	fmt.Println("\n== Installing /am:feedback slash command ==")
	if err := hook.InstallSlashCommand("feedback.md", []byte(hook.FeedbackSlashCommandContent)); err != nil {
		fmt.Printf("Slash command error: %v\n", err)
	}

	if autoUpdate {
		fmt.Println("\n== Setting up Auto-Update (LaunchAgent & Background Check) ==")
		if err := hook.SetupAutoUpdate(true); err != nil {
			fmt.Printf("Auto-update error: %v\n", err)
		} else {
			fmt.Println("✓ Đã kích hoạt tự động cập nhật (kiểm tra bản mới mỗi 6 tiếng qua LaunchAgent).")
		}
	} else if disableAutoUpdate {
		_ = hook.SetupAutoUpdate(false)
		fmt.Println("\n✓ Đã tắt tự động cập nhật.")
	} else if hook.IsAutoUpdateEnabled() {
		fmt.Println("\n✓ Tự động cập nhật hiện đang BẬT.")
	} else {
		fmt.Println("\n💡 Mẹo: Chạy `amux setup --auto-update` để tự động nâng cấp mỗi khi có bản mới.")
	}

	fmt.Println("\nsetup done. Open a new shell, then: amux add   (save your first account)")
}

func cmdHookInstall(args []string) {
	var target string
	if len(args) > 0 {
		target = hook.CanonicalTool(args[0])
		if target == "" {
			die("unknown tool/ide %q (supported: claude, agy, codex, cursor)", args[0])
		}
	}

	installClaude := false
	installAgy := false
	installCodex := false
	installCursor := false

	if target != "" {
		switch target {
		case "claude":
			installClaude = true
		case "agy":
			installAgy = true
		case "codex":
			installCodex = true
		case "cursor":
			installCursor = true
		}
	} else {
		installClaude = hook.ClaudeAvailable()
		installAgy = hook.GeminiAvailable()
		installCodex = hook.CodexAvailable()
		installCursor = hook.CursorAvailable()

		if !installClaude && !installAgy && !installCodex && !installCursor {
			installClaude = true
		}
	}

	installedCount := 0

	if installClaude {
		if err := hook.HookInstall(); err != nil {
			die("hook install (claude): %v", err)
		}
		fmt.Printf("installed hooks in %s\n  SessionStart -> amux hook claude start\n  SessionEnd   -> amux hook claude stop\n  Stop         -> amux hook claude stop\n\n", hook.ClaudeSettingsPath())
		installedCount++
	}

	if installAgy {
		if err := hook.GeminiHookInstall(); err != nil {
			fmt.Printf("warning: antigravity hook install: %v\n", err)
		} else {
			fmt.Printf("installed hooks in %s\n  SessionStart  -> amux hook agy start\n  PreInvocation -> amux hook agy start\n  Stop          -> amux hook agy stop\n\n", hook.GeminiHooksPath())
			installedCount++
		}
	}

	if installCodex {
		if err := hook.CodexHookInstall(); err != nil {
			fmt.Printf("warning: codex hook install: %v\n", err)
		} else {
			fmt.Printf("installed hooks in %s\n  SessionStart  -> amux hook codex start\n\n", hook.CodexHooksPath())
			installedCount++
		}
	}

	if installCursor {
		if err := hook.CursorHookInstall(); err != nil {
			fmt.Printf("warning: cursor hook install: %v\n", err)
		} else {
			fmt.Printf("installed hooks in %s\n  sessionStart  -> amux hook cursor start\n\n", hook.CursorHooksPath())
			installedCount++
		}
	}

	if installedCount == 0 {
		fmt.Println("no supported IDE/CLI found on this system.")
		return
	}

	// GUI-launched clients (Dock icon, IDE integration) never source shell
	// rc, so they'd miss ANTHROPIC_BASE_URL even with the rc line below —
	// mirror the same var into the macOS session env as a fallback.
	proxyUp := proxy.ProxyUp()
	hook.SyncLaunchctlEnv(proxyUp, proxy.ProxyBase())
	if proxyUp {
		fmt.Println("launchctl: mirrored ANTHROPIC_BASE_URL & GEMINI_API_BASE into macOS session env (proxy up)")
	} else {
		fmt.Println("launchctl: cleared gateway vars from macOS session env (proxy not running)")
	}

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
		if err := hook.AppendLine(rc, "\n# amux-accounts: route AI coding tools through the rotating proxy\n"+line+"\n"); err != nil {
			fmt.Printf("append failed: %v\n", err)
			return
		}
		fmt.Printf("appended. open a new shell (or `source %s`).\n", rc)
		return
	}
	fmt.Println("then open a new shell.")
}

func cmdHookUninstall(args []string) {
	var target string
	if len(args) > 0 {
		target = hook.CanonicalTool(args[0])
		if target == "" {
			die("unknown tool/ide %q (supported: claude, agy, codex, cursor)", args[0])
		}
	}

	unClaude := false
	unAgy := false
	unCodex := false
	unCursor := false

	if target != "" {
		switch target {
		case "claude":
			unClaude = true
		case "agy":
			unAgy = true
		case "codex":
			unCodex = true
		case "cursor":
			unCursor = true
		}
	} else {
		unClaude = hook.ClaudeAvailable() || hook.HookInstalled()
		unAgy = hook.GeminiAvailable() || hook.GeminiHookInstalled()
		unCodex = hook.CodexAvailable() || hook.CodexHookInstalled()
		unCursor = hook.CursorAvailable() || hook.CursorHookInstalled()
	}

	if unClaude {
		n, err := hook.HookUninstall()
		if err != nil {
			die("hook uninstall (claude): %v", err)
		}
		if n > 0 || target != "" {
			fmt.Printf("removed %d hook entr%s from %s\n", n, plural(n, "y", "ies"), hook.ClaudeSettingsPath())
		}
	}
	if unAgy {
		n, err := hook.GeminiHookUninstall()
		if err != nil {
			die("hook uninstall (antigravity): %v", err)
		}
		if n > 0 || target != "" {
			fmt.Printf("removed %d hook entr%s from %s\n", n, plural(n, "y", "ies"), hook.GeminiHooksPath())
		}
	}
	if unCodex {
		n, err := hook.CodexHookUninstall()
		if err != nil {
			die("hook uninstall (codex): %v", err)
		}
		if n > 0 || target != "" {
			fmt.Printf("removed %d hook entr%s from %s\n", n, plural(n, "y", "ies"), hook.CodexHooksPath())
		}
	}
	if unCursor {
		n, err := hook.CursorHookUninstall()
		if err != nil {
			die("hook uninstall (cursor): %v", err)
		}
		if n > 0 || target != "" {
			fmt.Printf("removed %d hook entr%s from %s\n", n, plural(n, "y", "ies"), hook.CursorHooksPath())
		}
	}
	fmt.Println("also remove the `eval \"$(am env)\"` line from your shell rc if you added it.")
}

func cmdHookStatus(args []string) {
	var target string
	if len(args) > 0 {
		target = hook.CanonicalTool(args[0])
		if target == "" {
			die("unknown tool/ide %q (supported: claude, agy, codex, cursor)", args[0])
		}
	}

	showClaude := target == "claude" || (target == "" && (hook.ClaudeAvailable() || hook.HookInstalled()))
	showAgy := target == "agy" || (target == "" && (hook.GeminiAvailable() || hook.GeminiHookInstalled()))
	showCodex := target == "codex" || (target == "" && (hook.CodexAvailable() || hook.CodexHookInstalled()))
	showCursor := target == "cursor" || (target == "" && (hook.CursorAvailable() || hook.CursorHookInstalled()))

	if showClaude {
		events := hook.InstalledEvents()
		if len(events) == 0 {
			fmt.Println("claude hooks: not installed")
		} else {
			for _, ev := range events {
				fmt.Printf("claude hook: %s -> amux hook claude\n", ev)
			}
		}
	}

	if showAgy {
		geminiEvents := hook.GeminiInstalledEvents()
		if len(geminiEvents) == 0 {
			fmt.Println("agy hooks: not installed")
		} else {
			for _, ev := range geminiEvents {
				fmt.Printf("agy hook: %s -> amux hook agy\n", ev)
			}
		}
	}

	if showCodex {
		if hook.CodexHookInstalled() {
			fmt.Println("codex hook: SessionStart -> amux hook codex")
		} else {
			fmt.Println("codex hooks: not installed")
		}
	}

	if showCursor {
		if hook.CursorHookInstalled() {
			fmt.Println("cursor hook: sessionStart -> amux hook cursor")
		} else {
			fmt.Println("cursor hooks: not installed")
		}
	}

	switch os.Getenv("ANTHROPIC_BASE_URL") {
	case proxy.ProxyBase():
		fmt.Printf("ANTHROPIC_BASE_URL: %s\n", proxy.ProxyBase())
	case "":
		fmt.Println("ANTHROPIC_BASE_URL: not set — claude will bypass the proxy (run `amux hook install` to wire `am env`)")
	default:
		fmt.Printf("ANTHROPIC_BASE_URL: %s  (not the proxy)\n", os.Getenv("ANTHROPIC_BASE_URL"))
	}

	switch os.Getenv("GEMINI_API_BASE") {
	case proxy.ProxyBase():
		fmt.Printf("GEMINI_API_BASE: %s\n", proxy.ProxyBase())
	case "":
		fmt.Println("GEMINI_API_BASE: not set — agy may bypass the proxy unless run via `am run agy`")
	default:
		fmt.Printf("GEMINI_API_BASE: %s  (not the proxy)\n", os.Getenv("GEMINI_API_BASE"))
	}

	if proxy.ProxyUp() {
		fmt.Println("proxy: running")
	} else {
		fmt.Println("proxy: not running (normal when no session is open)")
	}
}

func cmdHookTool(tool string, args []string) {
	op := "start"
	if len(args) > 0 {
		op = strings.ToLower(args[0])
	}

	isAgy := tool == "agy" || tool == "antigravity"

	switch op {
	case "stop", "down", "end", "session-end":
		proxy.RegisterSession(os.Getppid(), "end")
		if isAgy {
			fmt.Println("{}")
		}
	default:
		origStdout := os.Stdout
		var w *os.File
		var r *os.File
		if isAgy {
			r, w, _ = os.Pipe()
			os.Stdout = w
		}

		if !proxy.ProxyUp() {
			proxy.CmdProxyUpFlags(proxy.UpFlags{})
		}
		proxy.RegisterSession(os.Getppid(), "start")

		if isAgy {
			_ = w.Close()
			os.Stdout = origStdout
			_ = r.Close()
			fmt.Println("{}")
		}
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
	fromError := false
	var titleWords []string
	for _, a := range args {
		switch a {
		case "-b", "--bug":
			kind = "bug"
		case "-i", "--idea":
			kind = "idea"
		case "-e", "--error", "--latest-error":
			fromError = true
			kind = "bug"
		default:
			titleWords = append(titleWords, a)
		}
	}

	title := strings.Join(titleWords, " ")
	var body string

	if fromError {
		errLog, found := monitor.GetLatestErrorLog()
		if !found || strings.TrimSpace(errLog) == "" {
			fmt.Println("amux: no error logs found to report.")
			return
		}
		// Security layer: redact all sensitive info from error log before filing
		cleanErr, res := privacy.RedactString(errLog)
		if home := os.Getenv("HOME"); home != "" {
			cleanErr = strings.ReplaceAll(cleanErr, home, "~")
		}
		if title == "" {
			st := monitor.GetLogStats()
			if st.LastError != "" {
				firstLine := strings.Split(st.LastError, "\n")[0]
				title = fmt.Sprintf("[Bug Report] %s", firstLine)
			} else {
				title = "[Bug Report] Gateway error observed"
			}
		}
		var errB strings.Builder
		errB.WriteString("### Error Details (Sanitized)\n\n```\n")
		errB.WriteString(cleanErr)
		errB.WriteString("\n```\n\n")
		if res.Len() > 0 {
			errB.WriteString(fmt.Sprintf("> *Privacy filter: %s*\n\n", res.Summary()))
		}
		body = errB.String()
		fmt.Println("Extracted and sanitized latest error from log.")
	}

	if title == "" {
		fmt.Print("short title for the issue: ")
		r := bufio.NewReader(os.Stdin)
		line, _ := r.ReadString('\n')
		title = strings.TrimSpace(line)
		if title == "" {
			die("cancelled (no title given)")
		}
	}

	if body == "" {
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
		body = strings.Join(lines, "\n")
	}

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

	// Always apply privacy redaction layer to title and full body before creating GitHub issue
	cleanTitle, _ := privacy.RedactString(title)
	cleanBody, _ := privacy.RedactString(b.String())
	if home := os.Getenv("HOME"); home != "" {
		cleanTitle = strings.ReplaceAll(cleanTitle, home, "~")
		cleanBody = strings.ReplaceAll(cleanBody, home, "~")
	}

	label := "bug"
	if kind == "idea" {
		label = "enhancement"
	}

	ghPath, err := exec.LookPath("gh")
	if err == nil {
		cmd := exec.Command(ghPath, "issue", "create", "-R", feedbackRepo, "-t", cleanTitle, "-b", cleanBody, "-l", label)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Stdin = os.Stdin
		if err := cmd.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "amux: gh issue create failed (%v) — opening browser instead\n", err)
		} else {
			return
		}
	}

	u := fmt.Sprintf("https://github.com/%s/issues/new?title=%s&body=%s",
		feedbackRepo, url.QueryEscape(cleanTitle), url.QueryEscape(cleanBody))
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

func copyExecutable(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	dir := filepath.Dir(dst)
	_ = os.MkdirAll(dir, 0o755)

	// If directory is writable, use atomic rename via temp file
	tmpDst := filepath.Join(dir, fmt.Sprintf(".amux-tmp-%d", time.Now().UnixNano()))
	out, err := os.OpenFile(tmpDst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err == nil {
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			_ = os.Remove(tmpDst)
			return err
		}
		if err := out.Close(); err != nil {
			_ = os.Remove(tmpDst)
			return err
		}
		_ = os.Chmod(tmpDst, 0o755)
		return os.Rename(tmpDst, dst)
	}

	// Fallback: overwrite directly if dst itself is writable
	out, err = os.OpenFile(dst, os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

type versionInfo struct {
	Commit    string `json:"commit"`
	UpdatedAt string `json:"updated_at"`
}

func getInstalledCommit() string {
	home, _ := os.UserHomeDir()
	p := filepath.Join(home, ".am", "version.json")
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	var vi versionInfo
	if json.Unmarshal(b, &vi) == nil {
		return vi.Commit
	}
	return ""
}

func saveInstalledCommit(commit string) {
	home, _ := os.UserHomeDir()
	p := filepath.Join(home, ".am", "version.json")
	vi := versionInfo{
		Commit:    commit,
		UpdatedAt: time.Now().Format(time.RFC3339),
	}
	b, _ := json.MarshalIndent(vi, "", "  ")
	_ = os.WriteFile(p, b, 0o644)
}

func getRemoteHeadCommit(repoURL string) (string, error) {
	cmd := exec.Command("git", "ls-remote", repoURL, "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return "", fmt.Errorf("empty response from git ls-remote")
	}
	return fields[0], nil
}

func cmdUpdate(force, quiet bool) {
	if !quiet {
		fmt.Println("== Cập nhật amux lên phiên bản mới nhất ==")
	}
	if _, err := exec.LookPath("git"); err != nil {
		if !quiet {
			die("yêu cầu cài đặt 'git' trước khi cập nhật")
		}
		return
	}
	if _, err := exec.LookPath("go"); err != nil {
		if !quiet {
			die("yêu cầu cài đặt 'go' (>= 1.22) trước khi cập nhật")
		}
		return
	}

	repoURL := "https://github.com/ninhlee99/amux.git"
	remoteCommit, _ := getRemoteHeadCommit(repoURL)
	localCommit := getInstalledCommit()

	if !force && remoteCommit != "" && localCommit != "" && localCommit == remoteCommit {
		if quiet {
			return
		}
		short := remoteCommit
		if len(short) > 7 {
			short = short[:7]
		}
		fmt.Printf("amux đã ở phiên bản mới nhất (commit: %s). Dùng `amux update --force` nếu muốn build lại.\n", short)
		return
	}

	var buildDir string
	cwd, _ := os.Getwd()
	isLocalRepo := false
	if fi, err := os.Stat(filepath.Join(cwd, "main.go")); err == nil && !fi.IsDir() {
		if fi, err := os.Stat(filepath.Join(cwd, ".git")); err == nil && fi.IsDir() {
			isLocalRepo = true
		}
	}

	if isLocalRepo {
		if !quiet {
			fmt.Printf("Phát hiện mã nguồn tại %s, đang kiểm tra cập nhật (git pull)...\n", cwd)
		}
		pullCmd := exec.Command("git", "pull", "origin", "main")
		pullCmd.Dir = cwd
		if !quiet {
			pullCmd.Stdout = os.Stdout
			pullCmd.Stderr = os.Stderr
		}
		if err := pullCmd.Run(); err != nil && !quiet {
			fmt.Println("Cảnh báo: git pull thất bại, tiếp tục biên dịch từ source hiện tại...")
		}
		buildDir = cwd
	} else {
		tmp, err := os.MkdirTemp("", "amux-update-*")
		if err != nil {
			if !quiet {
				die("không thể tạo thư mục tạm: %v", err)
			}
			return
		}
		defer os.RemoveAll(tmp)

		if !quiet {
			fmt.Printf("Đang tải mã nguồn mới nhất từ %s...\n", repoURL)
		}
		cloneCmd := exec.Command("git", "clone", "--depth", "1", repoURL, filepath.Join(tmp, "amux"))
		if !quiet {
			cloneCmd.Stdout = os.Stdout
			cloneCmd.Stderr = os.Stderr
		}
		if err := cloneCmd.Run(); err != nil {
			if !quiet {
				die("tải mã nguồn thất bại: %v", err)
			}
			return
		}
		buildDir = filepath.Join(tmp, "amux")
	}

	if !quiet {
		fmt.Println("Đang biên dịch binary amux...")
	}
	tempBin := filepath.Join(os.TempDir(), fmt.Sprintf("am-build-%d", time.Now().UnixNano()))
	buildCmd := exec.Command("go", "build", "-o", tempBin, ".")
	buildCmd.Dir = buildDir
	if !quiet {
		buildCmd.Stdout = os.Stdout
		buildCmd.Stderr = os.Stderr
	}
	if err := buildCmd.Run(); err != nil {
		if !quiet {
			die("biên dịch thất bại: %v", err)
		}
		return
	}
	defer os.Remove(tempBin)

	home, _ := os.UserHomeDir()
	localBin := filepath.Join(home, ".local", "bin")
	_ = os.MkdirAll(localBin, 0o755)

	installPaths := []string{filepath.Join(localBin, "am")}

	if self, err := os.Executable(); err == nil {
		resolved, err := filepath.EvalSymlinks(self)
		if err == nil && resolved != "" && resolved != filepath.Join(localBin, "am") {
			installPaths = append(installPaths, resolved)
		}
	}

	usrLocalAm := "/usr/local/bin/am"
	if fi, err := os.Stat(usrLocalAm); err == nil && !fi.IsDir() {
		if f, err := os.OpenFile(usrLocalAm, os.O_WRONLY, 0); err == nil {
			f.Close()
			found := false
			for _, p := range installPaths {
				if p == usrLocalAm {
					found = true
					break
				}
			}
			if !found {
				installPaths = append(installPaths, usrLocalAm)
			}
		}
	}

	for _, p := range installPaths {
		if err := copyExecutable(tempBin, p); err != nil {
			if !quiet {
				fmt.Printf("Cảnh báo: Không thể ghi vào %s: %v\n", p, err)
			}
		} else {
			if !quiet {
				fmt.Printf("✓ Đã cập nhật binary: %s\n", p)
			}
			linkPath := filepath.Join(filepath.Dir(p), "amux")
			_ = os.Remove(linkPath)
			_ = os.Symlink(p, linkPath)
			if !quiet {
				fmt.Printf("✓ Đã liên kết alias: %s -> %s\n", linkPath, p)
			}
		}
	}

	if remoteCommit != "" {
		saveInstalledCommit(remoteCommit)
	} else if isLocalRepo {
		if out, err := exec.Command("git", "-C", cwd, "rev-parse", "HEAD").Output(); err == nil {
			saveInstalledCommit(strings.TrimSpace(string(out)))
		}
	}

	cmdSetup(nil)

	if proxy.ProxyUp() {
		if !quiet {
			fmt.Println("Đang khởi động lại proxy daemon với phiên bản mới...")
		}
		proxy.CmdProxyDown(true, true)
		time.Sleep(500 * time.Millisecond)
		proxy.CmdProxyUp()
	}

	if !quiet {
		fmt.Println("\n🎉 Cập nhật thành công! Dữ liệu hồ sơ & tài khoản tại ~/.am/ được giữ nguyên vẹn 100%.")
	}
}
