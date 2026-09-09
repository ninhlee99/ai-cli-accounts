package ui

import (
	"fmt"
	"os"

	"ai-cli-accounts/pkg/profile"
	"ai-cli-accounts/pkg/provider"
	"ai-cli-accounts/pkg/types"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

// PickProfile shows an arrow-key menu of profiles/providers and returns the chosen name.
func PickProfile(tool string) string {
	profs := profile.ListProfiles(tool)
	if tool == "claude" {
		profs = append(profs, providerMenuEntries()...)
	}
	if len(profs) == 0 {
		fmt.Fprintf(os.Stderr, "no %s profiles (add one: am add %s)\n", tool, tool)
		return ""
	}
	if len(profs) == 1 {
		return profs[0].Name
	}
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		fmt.Fprintf(os.Stderr, "am sw needs a name when not run in a terminal (e.g. am sw %s1)\n", tool)
		return ""
	}

	active := profile.ReadActivePointer(tool)
	cur := 0
	for i, p := range profs {
		if p.Name == active {
			cur = i
		}
	}

	old, err := term.MakeRaw(fd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "raw mode: %v\n", err)
		return ""
	}
	defer term.Restore(fd, old)

	render := func() {
		fmt.Fprintf(os.Stderr, "\r\x1b[Jpick a %s account  (↑/↓, Enter, q to cancel)\r\n", tool)
		for i, p := range profs {
			marker := "  "
			if i == cur {
				marker = "> "
			}
			tag := ""
			if p.Name == active {
				tag = "  (current)"
			}
			acct := p.Account
			if acct == "" {
				acct = "-"
			}
			fmt.Fprintf(os.Stderr, "%s\x1b[1m%-8s\x1b[0m %s%s\r\n", marker, p.ID, acct, tag)
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
			detail := p.BaseURL
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
