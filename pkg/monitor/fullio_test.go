package monitor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"amux-accounts/pkg/types"
)

func TestAppendFullIO_WritesUntruncatedBodies(t *testing.T) {
	t.Setenv("AM_HOME", t.TempDir())
	user := strings.Repeat("USER-LINE\n", 80) + "last user"
	ai := strings.Repeat("AI-LINE\n", 80) + "last ai"
	AppendFullIO(FullIO{
		Time:     time.Date(2026, 9, 12, 19, 0, 0, 0, time.UTC),
		Account:  "chatgpt:01",
		Dialect:  "claude",
		Messages: []types.ChatMessage{{Role: "user", Content: user}},
		Output:   ai,
		ToolCalls: []types.ToolCall{
			{ID: "toolu_1", Name: "Bash", Arguments: `{"command":"git diff"}`},
		},
	})
	b, err := os.ReadFile(filepath.Join(os.Getenv("AM_HOME"), "amux.log"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	if !strings.Contains(got, user) {
		t.Fatal("missing full user text")
	}
	if !strings.Contains(got, ai) {
		t.Fatal("missing full AI text")
	}
	if !strings.Contains(got, `{"command":"git diff"}`) {
		t.Fatal("missing tool args")
	}
	if !strings.Contains(got, "account=chatgpt:01") {
		t.Fatal(got)
	}
}
