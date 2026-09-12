package ui

import (
	"strconv"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// Tokyo-Night inspired palette (from amux-go reference TUI).
var (
	colBg      = lipgloss.Color("#0d1117")
	colPanel   = lipgloss.Color("#11161f")
	colBorder  = lipgloss.Color("#2a2f3d")
	colTextDim = lipgloss.Color("#565f89")
	colText    = lipgloss.Color("#c0caf5")
	colTextHi  = lipgloss.Color("#ffffff")
	colCyan    = lipgloss.Color("#7dcfff")
	colBlue    = lipgloss.Color("#7aa2f7")
	colPurple  = lipgloss.Color("#bb9af7")
	colGreen   = lipgloss.Color("#9ece6a")
	colYellow  = lipgloss.Color("#e0af68")
	colOrange  = lipgloss.Color("#ff9e64")
	colRed     = lipgloss.Color("#f7768e")
)

var (
	appStyle = lipgloss.NewStyle()

	headerBar = lipgloss.NewStyle().
			Padding(0, 2)

	brandStyle = lipgloss.NewStyle().
			Foreground(colCyan).
			Bold(true)

	brandSubStyle = lipgloss.NewStyle().
			Foreground(colTextDim)

	tabIdle = lipgloss.NewStyle().
		Foreground(colTextDim).
		Padding(0, 2)

	tabActive = lipgloss.NewStyle().
			Foreground(colTextHi).
			Background(colBlue).
			Bold(true).
			Padding(0, 2)

	footerBar = lipgloss.NewStyle().
			Foreground(colTextDim).
			Padding(0, 2)

	keyStyle = lipgloss.NewStyle().
			Foreground(colTextHi).
			Bold(true)

	panelBase = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colBorder).
			Padding(0, 1)

	panelTitleStyle = lipgloss.NewStyle().
			Foreground(colCyan).
			Bold(true).
			Padding(0, 1)

	labelStyle = lipgloss.NewStyle().Foreground(colTextDim)
	valStyle   = lipgloss.NewStyle().Foreground(colText)

	pillUp   = lipgloss.NewStyle().Foreground(colGreen).Bold(true)
	pillDown = lipgloss.NewStyle().Foreground(colRed).Bold(true)
	pillWarn = lipgloss.NewStyle().Foreground(colYellow).Bold(true)

	barFilled = lipgloss.NewStyle().Foreground(colCyan)
	barEmpty  = lipgloss.NewStyle().Foreground(colTextDim)

	rowDim = lipgloss.NewStyle().Foreground(colTextDim)
)

func watchPanel(title string, width, height int, accent lipgloss.Color, body string) string {
	style := panelBase.
		Width(maxInt(width-2, 10)).
		Height(maxInt(height-2, 3)).
		BorderForeground(accent)
	titled := panelTitleStyle.Foreground(accent).Render(title)
	inner := lipgloss.JoinVertical(lipgloss.Left, titled, body)
	return style.Render(inner)
}

func watchKV(label, val string) string {
	return labelStyle.Render(padRight(label, 9)) + valStyle.Render(val)
}

func watchBar(pct int, width int) string {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	if width < 4 {
		width = 4
	}
	filled := (pct * width) / 100
	if filled > width {
		filled = width
	}
	return barFilled.Render(repeatRune('█', filled)) + barEmpty.Render(repeatRune('░', width-filled))
}

func watchSparkline(values []int) string {
	if len(values) == 0 {
		return ""
	}
	ramp := []rune("▁▂▃▄▅▆▇█")
	maxV := 1
	for _, v := range values {
		if v > maxV {
			maxV = v
		}
	}
	out := make([]rune, 0, len(values))
	for _, v := range values {
		idx := (v * (len(ramp) - 1)) / maxV
		if idx < 0 {
			idx = 0
		}
		if idx >= len(ramp) {
			idx = len(ramp) - 1
		}
		out = append(out, ramp[idx])
	}
	return lipgloss.NewStyle().Foreground(colPurple).Render(string(out))
}

func statusPill(up bool) string {
	if up {
		return pillUp.Render("● UP")
	}
	return pillDown.Render("● DOWN")
}

func padRight(s string, n int) string {
	if len(s) >= n {
		return s
	}
	return s + repeatRune(' ', n-len(s))
}

func repeatRune(r rune, n int) string {
	if n <= 0 {
		return ""
	}
	b := make([]rune, n)
	for i := range b {
		b[i] = r
	}
	return string(b)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func watchTS(t time.Time) string {
	if t.IsZero() {
		return "--/-- --:--:--"
	}
	return t.Local().Format("02/01 15:04:05")
}

func commaInt(n int) string {
	neg := n < 0
	if neg {
		n = -n
	}
	s := strconv.Itoa(n)
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	if neg {
		return "-" + string(out)
	}
	return string(out)
}
