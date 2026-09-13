package monitor

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"amux-accounts/pkg/types"
)

func errorsLogPath() string { return filepath.Join(types.BaseDir(), "errors.log") }
func legacyLogPath() string { return filepath.Join(types.BaseDir(), "amux.log") }
func statsPath() string     { return filepath.Join(types.BaseDir(), "log_stats.json") }

// ErrorDiagnostic captures the complete request context and upstream response
// for a failed turn. It provides root-cause diagnostic information for debugging
// and for creating privacy-sanitized GitHub issues.
type ErrorDiagnostic struct {
	Time       time.Time
	Account    string
	Dialect    string
	Model      string
	Path       string
	Stop       string
	Error      string
	DurationMs int64
	Messages   []types.ChatMessage
	Output     string
	ToolCalls  []types.ToolCall
}

// FullIO is a backward-compatible type alias for ErrorDiagnostic.
type FullIO = ErrorDiagnostic

// RequestMetrics stores lightweight in-memory and on-disk counters so the CLI
// (e.g. `am logs`) and TUI dashboard can display statistics without reading large files.
type RequestMetrics struct {
	TotalRequests int64     `json:"total_requests"`
	TotalErrors   int64     `json:"total_errors"`
	LastError     string    `json:"last_error,omitempty"`
	LastErrorTime time.Time `json:"last_error_time,omitempty"`
	LastTurnTime  time.Time `json:"last_turn_time,omitempty"`
}

// LogStats is a backward-compatible alias for RequestMetrics.
type LogStats = RequestMetrics

var (
	statsMu       sync.Mutex
	cachedMetrics *RequestMetrics
	diagnosticAll = false
	pruneMu       sync.Mutex
	lastPruneTime time.Time
)

// SetDiagnosticAllBodies enables writing all payloads (not just errors) to disk.
// Default is false to prevent disk and memory bloat.
func SetDiagnosticAllBodies(v bool) {
	statsMu.Lock()
	defer statsMu.Unlock()
	diagnosticAll = v
}

// SetLogAllBodies is an alias for SetDiagnosticAllBodies.
func SetLogAllBodies(v bool) {
	SetDiagnosticAllBodies(v)
}

// ResetRequestMetrics clears in-memory cached metrics (useful for testing).
func ResetRequestMetrics() {
	statsMu.Lock()
	defer statsMu.Unlock()
	cachedMetrics = nil
}

// ResetStats is an alias for ResetRequestMetrics.
func ResetStats() {
	ResetRequestMetrics()
}

// GetRequestMetrics returns the current aggregate counts of requests and errors.
func GetRequestMetrics() RequestMetrics {
	statsMu.Lock()
	defer statsMu.Unlock()
	if cachedMetrics != nil {
		return *cachedMetrics
	}
	m := loadMetricsDisk()
	cachedMetrics = &m
	return m
}

// GetLogStats is an alias for GetRequestMetrics.
func GetLogStats() LogStats {
	return GetRequestMetrics()
}

func loadMetricsDisk() RequestMetrics {
	var m RequestMetrics
	b, err := os.ReadFile(statsPath())
	if err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

func saveMetricsDisk(m RequestMetrics) {
	b, err := json.Marshal(m)
	if err != nil {
		return
	}
	_ = os.WriteFile(statsPath(), b, 0o600)
}

// RecordErrorDiagnostic updates aggregate counters and ONLY writes the full turn
// to errors.log if an error occurred (or if diagnosticAll is explicitly enabled).
func RecordErrorDiagnostic(e ErrorDiagnostic) {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}

	isErr := strings.TrimSpace(e.Error) != ""

	// 1. Update lightweight aggregate counters
	statsMu.Lock()
	if cachedMetrics == nil {
		m := loadMetricsDisk()
		cachedMetrics = &m
	}
	cachedMetrics.TotalRequests++
	cachedMetrics.LastTurnTime = e.Time
	if isErr {
		cachedMetrics.TotalErrors++
		cachedMetrics.LastError = e.Error
		cachedMetrics.LastErrorTime = e.Time
	}
	currentMetrics := *cachedMetrics
	saveMetricsDisk(currentMetrics)
	shouldWriteBody := isErr || diagnosticAll || os.Getenv("AM_LOG_ALL") == "1"
	statsMu.Unlock()

	// 2. Trigger periodic 7-day log cleanup (at most once every 6 hours)
	maybeAutoPrune(7 * 24 * time.Hour)

	// 3. Skip writing to disk if the turn succeeded and full diagnostics are not requested
	if !shouldWriteBody {
		return
	}

	block := formatDiagnosticBlock(e)
	appendMu.Lock()
	defer appendMu.Unlock()

	targetPath := errorsLogPath()
	_ = os.MkdirAll(filepath.Dir(targetPath), 0o700)
	f, err := os.OpenFile(targetPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = f.WriteString(block)
	_ = f.Close()

	// Also write to legacy amux.log for backward compatibility
	if legacy := legacyLogPath(); legacy != targetPath {
		if lf, lerr := os.OpenFile(legacy, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); lerr == nil {
			_, _ = lf.WriteString(block)
			_ = lf.Close()
		}
	}
}

// AppendFullIO is an alias for RecordErrorDiagnostic.
func AppendFullIO(e FullIO) {
	RecordErrorDiagnostic(e)
}

// GetLatestErrorLog retrieves the most recent error block from errors.log (or amux.log).
func GetLatestErrorLog() (string, bool) {
	appendMu.Lock()
	defer appendMu.Unlock()

	paths := []string{errorsLogPath(), legacyLogPath()}
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		var blocks []string
		var current strings.Builder
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64*1024), 10<<20)

		for sc.Scan() {
			line := sc.Text()
			if strings.HasPrefix(line, "================================================================================") {
				if current.Len() > 0 {
					blocks = append(blocks, current.String())
					current.Reset()
				}
			}
			current.WriteString(line)
			current.WriteByte('\n')
		}
		_ = f.Close()
		if current.Len() > 0 {
			blocks = append(blocks, current.String())
		}

		for i := len(blocks) - 1; i >= 0; i-- {
			if strings.Contains(blocks[i], "error=") {
				return blocks[i], true
			}
		}
	}

	statsMu.Lock()
	defer statsMu.Unlock()
	if cachedMetrics != nil && cachedMetrics.LastError != "" {
		return fmt.Sprintf("Error: %s\nTime: %s", cachedMetrics.LastError, cachedMetrics.LastErrorTime.Format(time.RFC3339)), true
	}
	return "", false
}

func maybeAutoPrune(retention time.Duration) {
	pruneMu.Lock()
	defer pruneMu.Unlock()
	if time.Since(lastPruneTime) < 6*time.Hour {
		return
	}
	lastPruneTime = time.Now()
	go PruneLogs(retention)
}

// PruneLogs deletes log entries older than olderThan (e.g. 7 days) from errors.log,
// amux.log, requests.log, and events.log.
func PruneLogs(olderThan time.Duration) (int, error) {
	appendMu.Lock()
	defer appendMu.Unlock()

	cutoff := time.Now().Add(-olderThan)
	removedTotal := 0

	// 1. Prune errors.log and amux.log
	for _, p := range []string{errorsLogPath(), legacyLogPath()} {
		if fileExists(p) {
			if content, err := os.ReadFile(p); err == nil {
				blocks := strings.Split(string(content), "================================================================================\n")
				var kept []string
				for _, blk := range blocks {
					blk = strings.TrimSpace(blk)
					if blk == "" {
						continue
					}
					t := parseBlockTimestamp(blk)
					if !t.IsZero() && t.Before(cutoff) {
						removedTotal++
						continue
					}
					kept = append(kept, blk)
				}
				if len(kept) == 0 {
					_ = os.WriteFile(p, []byte(""), 0o600)
				} else {
					var b strings.Builder
					for _, k := range kept {
						b.WriteString("================================================================================\n")
						b.WriteString(k)
						b.WriteString("\n================================================================================\n\n")
					}
					_ = os.WriteFile(p, []byte(b.String()), 0o600)
				}
			}
		}
	}

	// 2. Prune requests.log
	if p := requestsPath(); fileExists(p) {
		lines := readJSONL(p)
		var kept []string
		for _, line := range lines {
			var e types.RequestEntry
			if err := json.Unmarshal([]byte(line), &e); err == nil {
				if !e.Time.IsZero() && e.Time.Before(cutoff) {
					removedTotal++
					continue
				}
			}
			kept = append(kept, line)
		}
		if len(kept) < len(lines) {
			_ = os.WriteFile(p, []byte(strings.Join(kept, "\n")+"\n"), 0o600)
		}
	}

	// 3. Prune events.log
	if p := eventsPath(); fileExists(p) {
		lines := readJSONL(p)
		var kept []string
		for _, line := range lines {
			var e types.EventEntry
			if err := json.Unmarshal([]byte(line), &e); err == nil {
				if !e.Time.IsZero() && e.Time.Before(cutoff) {
					removedTotal++
					continue
				}
			}
			kept = append(kept, line)
		}
		if len(kept) < len(lines) {
			_ = os.WriteFile(p, []byte(strings.Join(kept, "\n")+"\n"), 0o600)
		}
	}

	// 4. Prune old backup log files in ~/.am/
	entries, err := os.ReadDir(types.BaseDir())
	if err == nil {
		for _, de := range entries {
			name := de.Name()
			if strings.Contains(name, ".bak-") || strings.HasSuffix(name, ".old") {
				if info, err := de.Info(); err == nil && info.ModTime().Before(cutoff) {
					_ = os.Remove(filepath.Join(types.BaseDir(), name))
					removedTotal++
				}
			}
		}
	}

	return removedTotal, nil
}

func parseBlockTimestamp(blk string) time.Time {
	lines := strings.Split(blk, "\n")
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "t=") {
			parts := strings.Fields(l)
			for _, p := range parts {
				if strings.HasPrefix(p, "t=") {
					raw := strings.TrimPrefix(p, "t=")
					if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
						return t
					}
					if t, err := time.Parse(time.RFC3339, raw); err == nil {
						return t
					}
				}
			}
		}
	}
	return time.Time{}
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

func formatDiagnosticBlock(e ErrorDiagnostic) string {
	var b strings.Builder
	b.WriteString("================================================================================\n")
	fmt.Fprintf(&b, "t=%s  account=%s  dialect=%s  model=%s  path=%s  stop=%s  ms=%d\n",
		e.Time.Format(time.RFC3339Nano),
		dash(e.Account), dash(e.Dialect), dash(e.Model), dash(e.Path), dash(e.Stop), e.DurationMs)
	if e.Error != "" {
		fmt.Fprintf(&b, "error=%s\n", e.Error)
	}
	b.WriteString("--------------------------------------------------------------------------------\n")
	b.WriteString("REQUEST\n")
	if len(e.Messages) == 0 {
		b.WriteString("(empty)\n")
	} else {
		b.WriteString(formatMessages(e.Messages))
	}
	b.WriteString("--------------------------------------------------------------------------------\n")
	b.WriteString("RESPONSE\n")
	out := e.Output
	if strings.TrimSpace(out) == "" && len(e.ToolCalls) == 0 {
		b.WriteString("(empty)\n")
	} else if strings.TrimSpace(out) != "" {
		b.WriteString(out)
		if !strings.HasSuffix(out, "\n") {
			b.WriteByte('\n')
		}
	}
	for _, tc := range e.ToolCalls {
		fmt.Fprintf(&b, "[tool_call name=%s", tc.Name)
		if tc.ID != "" {
			fmt.Fprintf(&b, " id=%s", tc.ID)
		}
		b.WriteString("]\n")
		if strings.TrimSpace(tc.Arguments) != "" {
			b.WriteString(tc.Arguments)
			if !strings.HasSuffix(tc.Arguments, "\n") {
				b.WriteByte('\n')
			}
		}
	}
	b.WriteString("================================================================================\n\n")
	return b.String()
}

func formatMessages(msgs []types.ChatMessage) string {
	var b strings.Builder
	for _, m := range msgs {
		role := strings.TrimSpace(m.Role)
		if role == "" {
			role = "unknown"
		}
		fmt.Fprintf(&b, "[%s", role)
		if m.Name != "" {
			fmt.Fprintf(&b, " name=%s", m.Name)
		}
		if m.ToolCallID != "" {
			fmt.Fprintf(&b, " tool_call_id=%s", m.ToolCallID)
		}
		b.WriteString("]\n")
		if m.Content != "" {
			b.WriteString(m.Content)
			if !strings.HasSuffix(m.Content, "\n") {
				b.WriteByte('\n')
			}
		}
		for _, tc := range m.ToolCalls {
			fmt.Fprintf(&b, "[tool_call name=%s", tc.Name)
			if tc.ID != "" {
				fmt.Fprintf(&b, " id=%s", tc.ID)
			}
			b.WriteString("]\n")
			if strings.TrimSpace(tc.Arguments) != "" {
				b.WriteString(tc.Arguments)
				if !strings.HasSuffix(tc.Arguments, "\n") {
					b.WriteByte('\n')
				}
			}
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func dash(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "-"
	}
	return s
}
