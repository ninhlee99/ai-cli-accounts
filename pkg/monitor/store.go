package monitor

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"amux-accounts/pkg/types"
)

const (
	maxEventLines   = 1000
	maxRequestLines = 200
	previewRunes    = 120
	outputRunes     = 200
)

var (
	appendMu sync.Mutex
)

func eventsPath() string   { return filepath.Join(types.BaseDir(), "events.log") }
func requestsPath() string { return filepath.Join(types.BaseDir(), "requests.log") }

// AppendEvent writes one tagged event for the Logs tab.
func AppendEvent(tag, msg string) {
	e := types.EventEntry{Time: time.Now(), Tag: strings.TrimSpace(tag), Message: strings.TrimSpace(msg)}
	appendJSONL(eventsPath(), e, maxEventLines)
}

// AppendRequest writes one chat I/O record for the Requests tab.
func AppendRequest(e types.RequestEntry) {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	e.Input = TruncateRunes(e.Input, previewRunes)
	e.Output = TruncateRunes(e.Output, outputRunes)
	appendJSONL(requestsPath(), e, maxRequestLines)
}

// TruncateRunes shortens s to at most n runes with an ellipsis.
func TruncateRunes(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if n <= 0 || len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}

// LastUserText picks a preview from chat messages (last user / tool content).
func LastUserText(msgs []types.ChatMessage) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		role := strings.ToLower(m.Role)
		if role == "user" || role == "tool" {
			if m.Content != "" {
				return m.Content
			}
		}
	}
	if len(msgs) > 0 {
		return msgs[len(msgs)-1].Content
	}
	return ""
}

func appendJSONL(path string, v any, maxKeep int) {
	appendMu.Lock()
	defer appendMu.Unlock()
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = f.Write(append(b, '\n'))
	_ = f.Close()
	trimJSONL(path, maxKeep)
}

func trimJSONL(path string, maxKeep int) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		if t := sc.Text(); t != "" {
			lines = append(lines, t)
		}
	}
	_ = f.Close()
	if len(lines) <= maxKeep {
		return
	}
	lines = lines[len(lines)-maxKeep:]
	_ = os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
}

// LoadEvents returns recent events, newest last. tagFilter empty = all;
// otherwise case-insensitive substring match on tag or message.
func LoadEvents(limit int, filter string) []types.EventEntry {
	raw := readJSONL(eventsPath())
	filter = strings.ToLower(strings.TrimSpace(filter))
	var out []types.EventEntry
	for _, line := range raw {
		var e types.EventEntry
		if json.Unmarshal([]byte(line), &e) != nil {
			continue
		}
		if filter != "" {
			blob := strings.ToLower(e.Tag + " " + e.Message)
			if !strings.Contains(blob, filter) {
				continue
			}
		}
		out = append(out, e)
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

// LoadRequests returns recent request records, newest last.
func LoadRequests(limit int, filter string) []types.RequestEntry {
	raw := readJSONL(requestsPath())
	filter = strings.ToLower(strings.TrimSpace(filter))
	var out []types.RequestEntry
	for _, line := range raw {
		var e types.RequestEntry
		if json.Unmarshal([]byte(line), &e) != nil {
			continue
		}
		if filter != "" {
			blob := strings.ToLower(strings.Join([]string{
				e.Dialect, e.Account, e.Model, e.Path, e.Input, e.Output, e.Error, e.StopReason,
				e.ToolStatus, strings.Join(e.Tools, " "),
			}, " "))
			if !strings.Contains(blob, filter) {
				continue
			}
		}
		out = append(out, e)
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

func readJSONL(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		if t := sc.Text(); t != "" {
			lines = append(lines, t)
		}
	}
	return lines
}
