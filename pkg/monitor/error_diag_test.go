package monitor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"amux-accounts/pkg/types"
)

func TestRecordErrorDiagnostic_WritesUntruncatedBodiesOnErrors(t *testing.T) {
	t.Setenv("AM_HOME", t.TempDir())
	ResetRequestMetrics()
	user := strings.Repeat("USER-LINE\n", 80) + "last user"
	ai := strings.Repeat("AI-LINE\n", 80) + "last ai"
	RecordErrorDiagnostic(ErrorDiagnostic{
		Time:     time.Date(2026, 9, 12, 19, 0, 0, 0, time.UTC),
		Account:  "gemini:web:01",
		Dialect:  "claude",
		Error:    "test-error-500",
		Messages: []types.ChatMessage{{Role: "user", Content: user}},
		Output:   ai,
		ToolCalls: []types.ToolCall{
			{ID: "toolu_1", Name: "Bash", Arguments: `{"command":"git status"}`},
		},
	})
	b, err := os.ReadFile(filepath.Join(os.Getenv("AM_HOME"), "errors.log"))
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
	if !strings.Contains(got, `{"command":"git status"}`) {
		t.Fatal("missing tool args")
	}
	if !strings.Contains(got, "account=gemini:web:01") {
		t.Fatal(got)
	}
	if !strings.Contains(got, "error=test-error-500") {
		t.Fatal(got)
	}
}

func TestRecordErrorDiagnostic_SkipsNonErrorBodiesByDefault(t *testing.T) {
	t.Setenv("AM_HOME", t.TempDir())
	ResetRequestMetrics()
	SetDiagnosticAllBodies(false)
	RecordErrorDiagnostic(ErrorDiagnostic{
		Time:     time.Now(),
		Account:  "gemini:web:01",
		Dialect:  "claude",
		Messages: []types.ChatMessage{{Role: "user", Content: "hello"}},
		Output:   "hi there",
	})
	// errors.log should not be created for non-error requests
	if _, err := os.Stat(filepath.Join(os.Getenv("AM_HOME"), "errors.log")); err == nil {
		t.Fatal("errors.log should NOT exist for non-error entries")
	}
	st := GetRequestMetrics()
	if st.TotalRequests < 1 {
		t.Fatalf("expected TotalRequests >= 1, got %d", st.TotalRequests)
	}
	if st.TotalErrors != 0 {
		t.Fatalf("expected TotalErrors == 0, got %d", st.TotalErrors)
	}
}

func TestPruneLogs_RemovesEntriesOlderThan7Days(t *testing.T) {
	t.Setenv("AM_HOME", t.TempDir())
	ResetRequestMetrics()
	now := time.Now()
	oldTime := now.Add(-10 * 24 * time.Hour) // 10 days ago

	// Add an old error entry
	RecordErrorDiagnostic(ErrorDiagnostic{
		Time:    oldTime,
		Account: "old:01",
		Error:   "old error from 10 days ago",
	})
	// Add a recent error entry
	RecordErrorDiagnostic(ErrorDiagnostic{
		Time:    now,
		Account: "recent:01",
		Error:   "recent error from today",
	})

	removed, err := PruneLogs(7 * 24 * time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if removed < 1 {
		t.Fatalf("expected at least 1 removed old entry, got %d", removed)
	}

	b, _ := os.ReadFile(filepath.Join(os.Getenv("AM_HOME"), "errors.log"))
	got := string(b)
	if strings.Contains(got, "old:01") {
		t.Fatal("old entry should have been pruned")
	}
	if !strings.Contains(got, "recent:01") {
		t.Fatal("recent entry should have been preserved")
	}
}
