package ui

import (
	"fmt"
	"os"

	"amux-accounts/pkg/profile"
	"amux-accounts/pkg/provider"
	"amux-accounts/pkg/term"
	"amux-accounts/pkg/types"
	"golang.org/x/sys/unix"
	goterm "golang.org/x/term"
)

// PickProfile shows an arrow-key menu of profiles/providers and returns the chosen name.
func PickProfile(tool string) string {
	var profs []types.ProfileMeta
	for _, p := range profile.ListProfiles(tool) {
		if p.Disabled {
			continue
		}
		profs = append(profs, p)
	}
	if tool == "claude" {
		profs = append(profs, providerMenuEntries()...)
	}
	if len(profs) == 0 {
		term.Warn("no %s profiles (add one: am add %s)", tool, tool)
		return ""
	}
	if len(profs) == 1 {
		return profs[0].Name
	}
	fd := int(os.Stdin.Fd())
	if !goterm.IsTerminal(fd) {
		term.Warn("amux sw needs a name when not run in a terminal")
		return ""
	}

	active := profile.ReadActivePointer(tool)
	cur := 0
	for i, p := range profs {
		if p.Name == active {
			cur = i
		}
	}

	old, err := goterm.MakeRaw(fd)
	if err != nil {
		term.Error("raw mode: %v", err)
		return ""
	}
	defer goterm.Restore(fd, old)

	render := func() {
		fmt.Fprintf(os.Stderr, "\r\x1b[J%s  %s\r\n",
			term.CyanErr("amux sw"),
			term.DimErr("↑/↓  Enter  q"),
		)
		for i, p := range profs {
			acct := p.Account
			if acct == "" {
				acct = "-"
			}
			tag := ""
			if p.Name == active {
				tag = "  " + term.GreenErr("current")
			}
			if i == cur {
				fmt.Fprintf(os.Stderr, "%s %s  %s%s\r\n",
					term.CyanErr("❯"),
					term.BoldErr(fmt.Sprintf("%-10s", p.ID)),
					acct,
					tag,
				)
			} else {
				fmt.Fprintf(os.Stderr, "  %s  %s%s\r\n",
					term.DimErr(fmt.Sprintf("%-10s", p.ID)),
					term.DimErr(acct),
					tag,
				)
			}
		}
		fmt.Fprintf(os.Stderr, "\x1b[%dA", len(profs)+1)
	}
	clear := func() { fmt.Fprintf(os.Stderr, "\r\x1b[J") }

	render()
	buf := make([]byte, 3)
	for {
		n, err := unix.Read(fd, buf)
		if err != nil || n == 0 {
			clear()
			return ""
		}
		switch {
		case buf[0] == 3, buf[0] == 'q', buf[0] == 27 && n == 1:
			clear()
			return ""
		case buf[0] == '\r', buf[0] == '\n':
			clear()
			return profs[cur].Name
		case n == 3 && buf[0] == 27 && buf[1] == '[' && buf[2] == 'A':
			if cur > 0 {
				cur--
			}
			render()
		case n == 3 && buf[0] == 27 && buf[1] == '[' && buf[2] == 'B':
			if cur < len(profs)-1 {
				cur++
			}
			render()
		case buf[0] == 'k':
			if cur > 0 {
				cur--
			}
			render()
		case buf[0] == 'j':
			if cur < len(profs)-1 {
				cur++
			}
			render()
		}
	}
}

func providerMenuEntries() []types.ProfileMeta {
	var out []types.ProfileMeta
	f, err := provider.LoadConfigFile(provider.DefaultAccountsPath())
	if err == nil && f != nil {
		for _, p := range f.Providers {
			if !p.IsConfigured() {
				continue
			}
			detail := p.Account
			if detail == "" {
				detail = p.BaseURL
			}
			if detail == "" {
				detail = p.Model
			}
			out = append(out, types.ProfileMeta{
				Name:    p.ID,
				ID:      p.Type,
				Account: detail,
			})
		}
	}
	return out
}
