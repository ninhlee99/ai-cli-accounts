package main

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

// pickProfile shows an arrow-key menu of the tool's profiles and returns the
// chosen profile name. Returns "" if the user cancels (q / Esc / Ctrl-C) or
// stdin isn't a terminal.
func pickProfile(tool string) string {
	profs := listProfiles(tool)
	if len(profs) == 0 {
		die("no %s profiles (add one: am add %s)", tool, tool)
	}
	if len(profs) == 1 {
		return profs[0].Name
	}
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		die("am sw needs a name when not run in a terminal (e.g. am sw %s1)", tool)
	}

	active := readActivePointer(tool)
	cur := 0
	for i, p := range profs {
		if p.Name == active {
			cur = i
		}
	}

	old, err := term.MakeRaw(fd)
	if err != nil {
		die("raw mode: %v", err)
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
			fmt.Fprintf(os.Stderr, "%s\x1b[1m%-8s\x1b[0m %s%s\r\n", marker, p.ID, orDash(p.Account), tag)
		}
		// move cursor back up to the header for the next redraw
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
		case buf[0] == 3, buf[0] == 'q', buf[0] == 27 && n == 1: // Ctrl-C, q, bare Esc
			clear()
			return ""
		case buf[0] == '\r', buf[0] == '\n':
			clear()
			return profs[cur].Name
		case n == 3 && buf[0] == 27 && buf[1] == '[' && buf[2] == 'A': // up
			if cur > 0 {
				cur--
			}
			render()
		case n == 3 && buf[0] == 27 && buf[1] == '[' && buf[2] == 'B': // down
			if cur < len(profs)-1 {
				cur++
			}
			render()
		case buf[0] == 'k': // vim up
			if cur > 0 {
				cur--
			}
			render()
		case buf[0] == 'j': // vim down
			if cur < len(profs)-1 {
				cur++
			}
			render()
		}
	}
}
