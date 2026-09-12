package term

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// Event tags for realtime proxy/CLI monitoring on stderr.
const (
	TagProxy    = "PROXY"
	TagRotate   = "ROTATE"
	TagSwitch   = "SWITCH"
	TagFailover = "FAIL OVER"
	TagAuth     = "AUTH"
	TagPool     = "POOL"
	TagTool     = "TOOL"
	TagOK       = "OK"
	TagWarn     = "WARN"
	TagErr      = "ERROR"
	TagDegraded = "DEGRADED"
)

func tagPaint(tag string) string {
	t := fmt.Sprintf("%-9s", tag)
	if !errOK() {
		return "[" + strings.TrimSpace(tag) + "]"
	}
	switch tag {
	case TagOK:
		return GreenErr("[" + t + "]")
	case TagWarn, TagDegraded:
		return YellowErr("[" + t + "]")
	case TagErr, TagAuth:
		return RedErr("[" + t + "]")
	case TagRotate, TagSwitch:
		return MagentaErr("[" + t + "]")
	case TagFailover, TagPool:
		return CyanErr("[" + t + "]")
	case TagProxy, TagTool:
		return CyanErr("[" + t + "]")
	default:
		return DimErr("[" + t + "]")
	}
}

func stamp() string {
	s := time.Now().Format("15:04:05")
	if errOK() {
		return DimErr(s)
	}
	return s
}

// Log writes a tagged realtime line to stderr and persists it for `amux watch`.
//
//	15:04:05  amux  [ROTATE   ]  ninhle → tungnt  (rate-limit)
func Log(tag, format string, args ...any) {
	mu.Lock()
	defer mu.Unlock()
	msg := fmt.Sprintf(format, args...)
	if !quiet {
		prefix := CyanErr("amux")
		if !errOK() {
			prefix = "amux"
		}
		fmt.Fprintf(os.Stderr, "%s %s  %s  %s\n", stamp(), prefix, tagPaint(tag), msg)
	}
	persistEvent(tag, msg)
}

// persistEvent is set by monitor.EnableTermSink so term doesn't import monitor
// (monitor may import term). Default no-op.
var persistEvent = func(tag, msg string) {}

// SetEventSink registers a callback for Log persistence (used by monitor).
func SetEventSink(fn func(tag, msg string)) {
	if fn == nil {
		persistEvent = func(tag, msg string) {}
		return
	}
	persistEvent = fn
}

func LogProxy(format string, args ...any)    { Log(TagProxy, format, args...) }
func LogRotate(format string, args ...any)   { Log(TagRotate, format, args...) }
func LogSwitch(format string, args ...any)   { Log(TagSwitch, format, args...) }
func LogFailover(format string, args ...any) { Log(TagFailover, format, args...) }
func LogAuth(format string, args ...any)     { Log(TagAuth, format, args...) }
func LogPool(format string, args ...any)     { Log(TagPool, format, args...) }
func LogOK(format string, args ...any)       { Log(TagOK, format, args...) }
func LogWarn(format string, args ...any)     { Log(TagWarn, format, args...) }
func LogErr(format string, args ...any)      { Log(TagErr, format, args...) }
func LogDegraded(format string, args ...any) { Log(TagDegraded, format, args...) }
