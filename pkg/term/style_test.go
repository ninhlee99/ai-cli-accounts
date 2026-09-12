package term

import (
	"strings"
	"testing"
)

func TestProgressBarBounds(t *testing.T) {
	Disable()
	defer Enable()
	if n := len([]rune(ProgressBar(0, 10))); n != 10 {
		t.Fatalf("empty bar len=%d got %q", n, ProgressBar(0, 10))
	}
	if n := len([]rune(ProgressBar(1, 10))); n != 10 {
		t.Fatalf("full bar len=%d", n)
	}
	if got := ProgressBar(1, 10); got != strings.Repeat("#", 10) {
		t.Fatalf("full want all #, got %q", got)
	}
	if got := ProgressBar(0, 10); got != strings.Repeat(".", 10) {
		t.Fatalf("empty want all ., got %q", got)
	}
	if n := len([]rune(ProgressBar(0.5, 10))); n != 10 {
		t.Fatalf("half bar len=%d", n)
	}
}

func TestBadgeNoColor(t *testing.T) {
	Disable()
	defer Enable()
	if got := Badge("active", "active"); got != "[ACTIVE]" {
		t.Fatalf("got %q", got)
	}
}

func TestDetectThemeOverride(t *testing.T) {
	SetTheme("light")
	if cMuted == "\x1b[38;5;246m" {
		t.Fatal("light theme should not keep dark muted")
	}
	SetTheme("dark")
	if cAccent != "\x1b[38;5;80m" {
		t.Fatalf("dark accent = %q", cAccent)
	}
}

func TestBlockAlignment(t *testing.T) {
	Disable()
	defer Enable()
	// Top and bottom borders must match body width (blockW).
	topMid := blockW // '+' + mid + '+' where mid = innerW = blockW-2 → total blockW
	_ = topMid
	if innerW() != blockW-2 {
		t.Fatalf("innerW=%d want %d", innerW(), blockW-2)
	}
	s := padVisible("hi", 10)
	if visibleLen(s) != 10 {
		t.Fatalf("pad visibleLen=%d", visibleLen(s))
	}
	colored := "\x1b[36mhello\x1b[0m"
	if visibleLen(colored) != 5 {
		t.Fatalf("ansi visibleLen=%d", visibleLen(colored))
	}
}
