package monitor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"amux-accounts/pkg/types"
)

func amuxLogPath() string { return filepath.Join(types.BaseDir(), "amux.log") }

// FullIO is one untruncated request/response turn for ~/.am/amux.log.
type FullIO struct {
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

// AppendFullIO writes the complete user request + model reply to amux.log.
// Never truncates body text. Append-only.
func AppendFullIO(e FullIO) {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	block := formatFullIO(e)
	appendMu.Lock()
	defer appendMu.Unlock()
	_ = os.MkdirAll(filepath.Dir(amuxLogPath()), 0o700)
	f, err := os.OpenFile(amuxLogPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = f.WriteString(block)
	_ = f.Close()
}

func formatFullIO(e FullIO) string {
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
