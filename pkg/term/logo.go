package term

import "fmt"

// Large slant AMUX — fixed brand mark (chat + watch).
var amuxWordmark = []string{
	`    ___    __  ___  __  ___  _  __`,
	`   /   |  /  |/  / / / / /| | |/ /`,
	`  / /| | / /|_/ / / / / / | |   / `,
	` / ___ |/ /  / /  / /_/ /|  /   | `,
	`/_/  |_|/_/  /_/  \____/_| /_/|_| `,
}

// LogoSmall prints the slant wordmark.
func LogoSmall() {
	for _, line := range amuxWordmark {
		fmt.Println(Cyan(Bold("  " + line)))
	}
}

// LogoBanner prints fixed AMUX wordmark for watch — same every redraw.
func LogoBanner() {
	LogoSmall()
	fmt.Println(Dim("  gateway · rotate · pool · watch"))
}

// LogoMark returns a one-line brand prefix.
func LogoMark() string {
	return Cyan(Bold("amux"))
}
