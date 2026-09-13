package cli

import (
	"fmt"
	"strconv"
	"strings"

	"amux-accounts/pkg/profile"
	"amux-accounts/pkg/provider"
	"amux-accounts/pkg/proxy"
	"amux-accounts/pkg/ui"
)

type accountRef struct {
	kind string // "claude" | "provider"
	id   string // profile name or provider id
}

func cmdToggleAccount(q string, enabled bool) {
	ref, err := resolveAccount(q)
	if err != nil {
		die("%v", err)
	}
	if err := applyAccountEnabled(ref, enabled); err != nil {
		die("%v", err)
	}
	proxy.Sync()
	word := "off"
	if enabled {
		word = "on"
	}
	fmt.Printf("%s %s — %s\n", word, ref.id, accountToggleHint(ref, enabled))
}

func accountToggleHint(ref accountRef, enabled bool) string {
	if enabled {
		return "in rotate (am pool remove " + ref.id + " to take out)"
	}
	return "out of rotate, still in am accounts (am on " + ref.id + " to restore)"
}

func applyAccountEnabled(ref accountRef, enabled bool) error {
	switch ref.kind {
	case "provider":
		return provider.SetEnabled(provider.DefaultAccountsPath(), ref.id, enabled)
	case "claude":
		return profile.SetDisabled("claude", ref.id, !enabled)
	default:
		return fmt.Errorf("unknown account kind %q", ref.kind)
	}
}

func resolveAccount(q string) (accountRef, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return accountRef{}, fmt.Errorf("usage: am off|on <id>")
	}
	if id, err := provider.MatchID(provider.DefaultAccountsPath(), q); err == nil {
		return accountRef{kind: "provider", id: id}, nil
	} else if strings.Contains(err.Error(), "ambiguous") {
		return accountRef{}, err
	}
	if name, ok := lookupClaudeProfile(q); ok {
		return accountRef{kind: "claude", id: name}, nil
	}
	return accountRef{}, fmt.Errorf("no account matching %q (see: am accounts)", q)
}

func lookupClaudeProfile(q string) (string, bool) {
	profs := profile.ListProfiles("claude")
	for _, p := range profs {
		if strings.EqualFold(p.ID, q) || p.Name == q {
			return p.Name, true
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
	if len(hits) == 1 {
		return hits[0], true
	}
	return "", false
}

func cmdPool(args []string) {
	if len(args) == 0 {
		ui.CmdPool()
		return
	}
	switch args[0] {
	case "ls", "list":
		ui.CmdPool()
	case "set":
		cmdPoolSet(args[1:])
	case "add":
		if len(args) < 2 {
			die("usage: am pool add <id>")
		}
		cmdToggleAccount(strings.Join(args[1:], " "), true)
	case "remove", "rm":
		if len(args) < 2 {
			die("usage: am pool remove <id>")
		}
		cmdToggleAccount(strings.Join(args[1:], " "), false)
	case "priority":
		ui.CmdAccountsCmd(args)
	case "model":
		ui.CmdAccountsCmd(args)
	default:
		die("usage: am pool [set <id> [--priority N] [--model M] [--on|--off] | add|remove|priority|model] …")
	}
}

func cmdPoolSet(args []string) {
	if len(args) == 0 {
		die("usage: am pool set <id> [--priority <N>] [--model <model>] [--on|--off]")
	}
	id := args[0]
	var priority *int
	var model *string
	var toggle *bool

	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--priority", "-p":
			if i+1 < len(args) {
				n, err := strconv.Atoi(args[i+1])
				if err != nil {
					die("invalid priority %q: %v", args[i+1], err)
				}
				priority = &n
				i++
			}
		case "--model", "-m":
			if i+1 < len(args) {
				m := args[i+1]
				model = &m
				i++
			}
		case "--on", "--enable":
			t := true
			toggle = &t
		case "--off", "--disable":
			t := false
			toggle = &t
		}
	}

	ref, err := resolveAccount(id)
	if err != nil {
		die("%v", err)
	}

	accPath := provider.DefaultAccountsPath()
	if priority != nil {
		if err := provider.SetPriority(accPath, ref.id, *priority); err != nil {
			die("set priority: %v", err)
		}
		fmt.Printf("set %s priority -> %d\n", ref.id, *priority)
	}
	if model != nil {
		if err := provider.SetModel(accPath, ref.id, *model); err != nil {
			die("set model: %v", err)
		}
		fmt.Printf("set %s model -> %s\n", ref.id, *model)
	}
	if toggle != nil {
		if err := applyAccountEnabled(ref, *toggle); err != nil {
			die("toggle account: %v", err)
		}
		word := "off"
		if *toggle {
			word = "on"
		}
		fmt.Printf("%s %s\n", word, ref.id)
	}
	proxy.Sync()
}

