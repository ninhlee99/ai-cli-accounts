package term

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/term"
)

const (
	reset  = "\x1b[0m"
	bold   = "\x1b[1m"
	italic = "\x1b[3m"

	// Standard 8-color (readable on light + dark).
	fgRed     = "\x1b[31m"
	fgGreen   = "\x1b[32m"
	fgYellow  = "\x1b[33m"
	fgBlue    = "\x1b[34m"
	fgMagenta = "\x1b[35m"
	fgCyan    = "\x1b[36m"
	fgWhite   = "\x1b[37m"

	bgCyan  = "\x1b[46m"
	bgGray  = "\x1b[100m"
	bgGreen = "\x1b[42m"
)

var (
	mu       sync.Mutex
	forceOff bool
	quiet    bool

	// 256-color palette — mid tones, work on both dark and light bg.
	cAccent = "\x1b[38;5;37m"  // teal
	cMuted  = "\x1b[38;5;245m" // mid gray
	cOK     = "\x1b[38;5;34m"  // green
	cWarn   = "\x1b[38;5;178m" // amber
	cErr    = "\x1b[38;5;167m" // soft red
	cBright = "\x1b[38;5;231m" // near-white (dark mode text)
	cInk    = "\x1b[38;5;232m" // near-black (light mode on badges)
)

func init() {
	applyTheme(detectDark())
}

// detectDark: AMUX_THEME=dark|light overrides; else COLORFGBG; default dark.
func detectDark() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("AMUX_THEME"))) {
	case "light":
		return false
	case "dark":
		return true
	}
	cfg := os.Getenv("COLORFGBG")
	if cfg == "" {
		return true
	}
	// COLORFGBG = "fg;bg" — bg 0–7 classic, or 0–255. High bg → light terminal.
	parts := strings.Split(cfg, ";")
	if len(parts) < 2 {
		return true
	}
	bg, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil {
		return true
	}
	if bg >= 0 && bg <= 7 {
		// 0 black … 7 white (xterm classic)
		return bg <= 6
	}
	// 256-color bg index
	return bg < 8 || (bg >= 232 && bg <= 243)
}

func applyTheme(dark bool) {
	if dark {
		cAccent = "\x1b[38;5;80m"  // soft cyan
		cMuted = "\x1b[38;5;246m"  // readable gray on black
		cOK = "\x1b[38;5;114m"
		cWarn = "\x1b[38;5;221m"
		cErr = "\x1b[38;5;203m"
		cBright = "\x1b[38;5;255m"
		cInk = "\x1b[38;5;16m"
	} else {
		cAccent = "\x1b[38;5;30m"  // deep teal on white
		cMuted = "\x1b[38;5;240m"  // dark gray on white
		cOK = "\x1b[38;5;28m"
		cWarn = "\x1b[38;5;130m"
		cErr = "\x1b[38;5;160m"
		cBright = "\x1b[38;5;16m"
		cInk = "\x1b[38;5;16m"
	}
}

// SetTheme forces "dark" or "light" (tests / AMUX_THEME).
func SetTheme(name string) {
	mu.Lock()
	defer mu.Unlock()
	applyTheme(strings.EqualFold(name, "dark"))
}

// Disable turns colors off (tests / piped output).
func Disable() { forceOff = true }

// Enable re-enables auto detection.
func Enable() { forceOff = false }

// SetQuiet suppresses tagged Log lines on stderr (chat UI). Events still persist.
func SetQuiet(v bool) {
	mu.Lock()
	quiet = v
	mu.Unlock()
}

func colorEnabled(fd int) bool {
	if forceOff {
		return false
	}
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if os.Getenv("AMUX_COLOR") == "0" {
		return false
	}
	if os.Getenv("AMUX_COLOR") == "1" || os.Getenv("FORCE_COLOR") != "" {
		return true
	}
	return term.IsTerminal(fd)
}

func outOK() bool { return colorEnabled(int(os.Stdout.Fd())) }
func errOK() bool { return colorEnabled(int(os.Stderr.Fd())) }

func paint(enabled bool, code, s string) string {
	if !enabled || s == "" {
		return s
	}
	return code + s + reset
}

// Bold / Dim / Cyan / … paint for stdout.
func Bold(s string) string    { return paint(outOK(), bold, s) }
func Dim(s string) string     { return paint(outOK(), cMuted, s) }
func Cyan(s string) string    { return paint(outOK(), cAccent, s) }
func Green(s string) string   { return paint(outOK(), cOK, s) }
func Yellow(s string) string  { return paint(outOK(), cWarn, s) }
func Red(s string) string     { return paint(outOK(), cErr, s) }
func Magenta(s string) string { return paint(outOK(), fgMagenta, s) }
func Blue(s string) string    { return paint(outOK(), fgBlue, s) }
func White(s string) string   { return paint(outOK(), cBright, s) }
func Gray(s string) string    { return paint(outOK(), cMuted, s) }

// BoldErr paints for stderr (proxy logs).
func BoldErr(s string) string    { return paint(errOK(), bold, s) }
func CyanErr(s string) string    { return paint(errOK(), cAccent, s) }
func GreenErr(s string) string   { return paint(errOK(), cOK, s) }
func YellowErr(s string) string  { return paint(errOK(), cWarn, s) }
func RedErr(s string) string     { return paint(errOK(), cErr, s) }
func DimErr(s string) string     { return paint(errOK(), cMuted, s) }
func MagentaErr(s string) string { return paint(errOK(), fgMagenta, s) }

// Dot serving / idle / off.
func DotLive() string {
	if outOK() {
		return cOK + "●" + reset
	}
	return "●"
}
func DotIdle() string {
	if outOK() {
		return cMuted + "○" + reset
	}
	return "○"
}
func DotWarn() string {
	if outOK() {
		return cWarn + "●" + reset
	}
	return "●"
}
func DotDead() string {
	if outOK() {
		return cErr + "●" + reset
	}
	return "●"
}

// Badge renders a status chip. Short labels (<=4) are padded so columns align.
func Badge(kind, text string) string {
	t := strings.ToUpper(strings.TrimSpace(text))
	if len(t) <= 4 {
		t = fmt.Sprintf("%-4s", t)
	}
	if !outOK() {
		return "[" + strings.TrimSpace(t) + "]"
	}
	var fg, bg string
	switch kind {
	case "ok", "active", "live":
		fg, bg = bold+cInk, bgGreen
	case "warn", "cooldown":
		fg, bg = bold+cInk, "\x1b[43m"
	case "err", "dead", "fail":
		fg, bg = bold+"\x1b[97m", "\x1b[41m"
	case "off", "idle":
		fg, bg = bold+"\x1b[97m", bgGray
	case "info", "proxy":
		fg, bg = bold+cInk, bgCyan
	default:
		fg, bg = bold+"\x1b[97m", bgGray
	}
	return bg + fg + " " + t + " " + reset
}

// ProgressBar renders a filled bar for pct in [0,1].
// Uses ASCII #/. so every terminal font renders correctly.
func ProgressBar(pct float64, width int) string {
	if width <= 0 {
		width = 12
	}
	if pct < 0 {
		pct = 0
	}
	if pct > 1 {
		pct = 1
	}
	filled := int(pct*float64(width) + 0.5)
	if filled > width {
		filled = width
	}
	bar := strings.Repeat("#", filled) + strings.Repeat(".", width-filled)
	if !outOK() {
		return bar
	}
	col := cOK
	switch {
	case pct >= 0.9:
		col = cErr
	case pct >= 0.7:
		col = cWarn
	case pct >= 0.4:
		col = cAccent
	}
	return col + bar + reset
}

const (
	kvLabelW = 10
	blockW   = 58 // total chars between leading indent and newline, including both borders
)

// VisibleLen counts printable runes, ignoring ANSI escapes.
func VisibleLen(s string) int { return visibleLen(s) }

// visibleLen counts runes ignoring ANSI CSI sequences.
func visibleLen(s string) int {
	n := 0
	inEsc := false
	for _, r := range s {
		if inEsc {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEsc = false
			}
			continue
		}
		if r == '\x1b' {
			inEsc = true
			continue
		}
		n++
	}
	return n
}

func padVisible(s string, width int) string {
	n := visibleLen(s)
	if n >= width {
		return s
	}
	return s + strings.Repeat(" ", width-n)
}

func truncateVisible(s string, width int) string {
	if visibleLen(s) <= width {
		return s
	}
	var b strings.Builder
	n := 0
	inEsc := false
	for _, r := range s {
		if inEsc {
			b.WriteRune(r)
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEsc = false
			}
			continue
		}
		if r == '\x1b' {
			inEsc = true
			b.WriteRune(r)
			continue
		}
		if n >= width-3 {
			b.WriteString("...")
			break
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}

func innerW() int { return blockW - 2 }

// Section opens a closed ASCII block with a titled top border.
func Section(title string) {
	fmt.Println()
	t := " " + strings.ToUpper(strings.TrimSpace(title)) + " "
	mid := innerW()
	if len(t) > mid-4 {
		t = t[:mid-4] + " "
	}
	left := 2
	right := mid - left - len(t)
	if right < 1 {
		right = 1
		left = mid - len(t) - 1
		if left < 1 {
			left = 1
		}
	}
	top := "+" + strings.Repeat("-", left) + t + strings.Repeat("-", right) + "+"
	fmt.Println(Cyan(Bold("  " + top)))
}

// PanelEnd closes the current block.
func PanelEnd() {
	fmt.Println(Cyan("  +" + strings.Repeat("-", innerW()) + "+"))
}

// Header prints a modern banner block.
func Header(title, subtitle string) {
	Section(title)
	if subtitle != "" {
		Row(Dim(subtitle))
	}
	PanelEnd()
}

// KV prints key/value inside the open block, right border aligned.
func KV(key, value string) {
	label := fmt.Sprintf("%-*s", kvLabelW, key)
	Row(Dim(label) + "  " + value)
}

// Row prints one content line inside the open block (padded + right border).
func Row(s string) {
	w := innerW()
	s = truncateVisible(s, w)
	s = padVisible(s, w)
	fmt.Printf("  %s%s%s\n", Cyan("|"), s, Cyan("|"))
}

// BlankRow prints an empty padded line inside the block.
func BlankRow() {
	Row("")
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// Success / Warn / Error / Info one-liners for CLI.
func Success(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	fmt.Println(Green("✓ ") + msg)
}
func Warn(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	fmt.Println(Yellow("! ") + msg)
}
func Error(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	fmt.Println(Red("x ") + msg)
}
func Info(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	fmt.Println(Cyan("> ") + msg)
}

// Printf is fmt.Printf with no styling (helper for consistency).
func Printf(format string, args ...any) { fmt.Printf(format, args...) }
