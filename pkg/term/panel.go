package term

import (
	"fmt"
	"os"
	"strings"

	goterm "golang.org/x/term"
)

// TermWidth returns stdout width (fallback 100).
func TermWidth() int {
	w, _, err := goterm.GetSize(int(os.Stdout.Fd()))
	if err != nil || w < 40 {
		return 100
	}
	return w
}

// BuildPanel returns a closed panel; every line has exactly `width` visible cols.
func BuildPanel(title string, body []string, width int) []string {
	if width < 20 {
		width = 20
	}
	inner := width - 2
	t := " " + strings.ToLower(strings.TrimSpace(title)) + " "
	if len(t) > inner-4 {
		t = t[:inner-4] + " "
	}
	left := 2
	right := inner - left - len(t)
	if right < 1 {
		right = 1
		left = inner - len(t) - right
		if left < 1 {
			left = 1
			right = inner - len(t) - left
			if right < 0 {
				right = 0
			}
		}
	}

	top := Dim("+"+strings.Repeat("-", left)) + Cyan(Bold(t)) + Dim(strings.Repeat("-", right)+"+")
	bot := Dim("+" + strings.Repeat("-", inner) + "+")

	out := make([]string, 0, len(body)+2)
	out = append(out, ensureVisWidth(top, width))
	if len(body) == 0 {
		out = append(out, panelRow("", inner, width))
	}
	for _, line := range body {
		out = append(out, panelRow(line, inner, width))
	}
	out = append(out, ensureVisWidth(bot, width))
	return out
}

func panelRow(content string, inner, width int) string {
	content = truncateVisible(content, inner)
	content = padVisible(content, inner)
	return ensureVisWidth(Dim("|")+content+Dim("|"), width)
}

func ensureVisWidth(s string, width int) string {
	n := visibleLen(s)
	if n == width {
		return s
	}
	if n < width {
		return s + strings.Repeat(" ", width-n)
	}
	return truncateVisible(s, width)
}

// JoinColumns merges panels side-by-side; pads short panels with blank space.
func JoinColumns(gap int, cols ...[]string) []string {
	if len(cols) == 0 {
		return nil
	}
	h := 0
	widths := make([]int, len(cols))
	for i, c := range cols {
		if len(c) > h {
			h = len(c)
		}
		if len(c) > 0 {
			widths[i] = visibleLen(c[0])
		}
	}
	gapStr := strings.Repeat(" ", gap)
	out := make([]string, h)
	for row := 0; row < h; row++ {
		var b strings.Builder
		for i, c := range cols {
			if i > 0 {
				b.WriteString(gapStr)
			}
			if row < len(c) {
				b.WriteString(ensureVisWidth(c[row], widths[i]))
			} else {
				b.WriteString(strings.Repeat(" ", widths[i]))
			}
		}
		out[row] = b.String()
	}
	return out
}

// PrintLines prints lines with indent.
func PrintLines(lines []string, indent string) {
	for _, line := range lines {
		fmt.Println(indent + line)
	}
}

// KVLine formats dim label + value.
func KVLine(key, value string) string {
	if key == "" {
		return strings.Repeat(" ", kvLabelW) + "  " + value
	}
	return Dim(fmt.Sprintf("%-*s", kvLabelW, key)) + "  " + value
}

// GridCols picks column count from terminal width.
func GridCols(termW int) int {
	switch {
	case termW >= 120:
		return 3
	case termW >= 80:
		return 2
	default:
		return 1
	}
}

// GridColWidth splits terminal into n columns.
func GridColWidth(termW, cols, gap, indent int) int {
	if cols < 1 {
		cols = 1
	}
	usable := termW - indent - gap*(cols-1)
	w := usable / cols
	if w < 22 {
		w = 22
	}
	for cols*w+gap*(cols-1)+indent > termW && w > 22 {
		w--
	}
	return w
}

// ShareBar draws a stacked proportion bar.
func ShareBar(values []int, width int) string {
	if width < 8 {
		width = 8
	}
	sum := 0
	for _, v := range values {
		if v > 0 {
			sum += v
		}
	}
	if sum == 0 {
		return Dim(strings.Repeat(".", width))
	}
	colors := []func(string) string{Yellow, Green, Cyan, Magenta, Red}
	var b strings.Builder
	filled := 0
	for i, v := range values {
		if v <= 0 {
			continue
		}
		n := int(float64(v)/float64(sum)*float64(width) + 0.5)
		if n < 1 {
			n = 1
		}
		if filled+n > width {
			n = width - filled
		}
		if n <= 0 {
			continue
		}
		b.WriteString(colors[i%len(colors)](strings.Repeat("#", n)))
		filled += n
	}
	if filled < width {
		b.WriteString(Dim(strings.Repeat(".", width-filled)))
	}
	return b.String()
}

// Sparkline renders values as block spark chars.
func Sparkline(vals []float64, width int) string {
	if width < 4 {
		width = 4
	}
	if len(vals) == 0 {
		return Dim(strings.Repeat(".", width))
	}
	sampled := make([]float64, width)
	for i := 0; i < width; i++ {
		idx := i * len(vals) / width
		if idx >= len(vals) {
			idx = len(vals) - 1
		}
		sampled[i] = vals[idx]
	}
	minV, maxV := sampled[0], sampled[0]
	for _, v := range sampled {
		if v < minV {
			minV = v
		}
		if v > maxV {
			maxV = v
		}
	}
	// ASCII-safe spark (no unicode block — avoids font break)
	levels := []byte{'_', '.', ':', '-', '=', '+', '*', '#'}
	var b strings.Builder
	for _, v := range sampled {
		level := 0
		if maxV > minV {
			level = int((v - minV) / (maxV - minV) * float64(len(levels)-1))
		} else if v > 0 {
			level = len(levels) - 1
		}
		if level < 0 {
			level = 0
		}
		if level >= len(levels) {
			level = len(levels) - 1
		}
		b.WriteString(Cyan(string(levels[level])))
	}
	return b.String()
}
