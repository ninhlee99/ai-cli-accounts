package cli

import (
	"fmt"
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
		die("usage: am pool [add|remove|priority|model] …")
	}
}
