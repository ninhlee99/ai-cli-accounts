package tools

import (
	"strings"
	"testing"

	"amux-accounts/pkg/types"
)

func TestParseWebTools_AMUXAndBash(t *testing.T) {
	defs := []types.ToolDef{{Name: "Bash"}, {Name: "Read"}}
	text := `
I'll list files.
<<<AMUX_TOOL name="Read" id="toolu_web_1">>>
{"path":"README.md"}
<<<END_AMUX_TOOL>>>
`
	calls := ParseWebTools(text, defs)
	if len(calls) != 1 || calls[0].Name != "Read" {
		t.Fatalf("amux block: %+v", calls)
	}
	if !strings.Contains(calls[0].Arguments, "README.md") {
		t.Fatalf("args: %s", calls[0].Arguments)
	}

	xml := ParseWebTools(`<tool_call>
{"name": "Bash", "arguments": {"command": "git status"}}
</tool_call>`, defs)
	if len(xml) != 1 || xml[0].Name != "Bash" || !strings.Contains(xml[0].Arguments, "git status") {
		t.Fatalf("xml tool_call: %+v", xml)
	}

	bash := ParseWebTools("run:\n```bash\ngit diff -- README.md\n```\n", defs)
	if len(bash) != 1 || bash[0].Name != "Bash" {
		t.Fatalf("bash fence: %+v", bash)
	}
	if !strings.Contains(bash[0].Arguments, "git diff") {
		t.Fatalf("bash args: %s", bash[0].Arguments)
	}
}

func TestParseWebTools_RejectsUnknown(t *testing.T) {
	defs := []types.ToolDef{{Name: "Read"}}
	calls := ParseWebTools("```bash\nrm -rf /\n```", defs)
	if len(calls) != 0 {
		t.Fatalf("bash must not map when Bash not in catalog: %+v", calls)
	}
}

func TestFormatToolCalls(t *testing.T) {
	got := FormatToolCalls([]types.ToolCall{
		{Name: "Bash", Arguments: `{"command":"git diff -- README.md"}`},
	})
	if !strings.Contains(got, "Bash") || !strings.Contains(got, "git diff") {
		t.Fatalf("format: %s", got)
	}
}

func TestWebPreamble_IncludesSchemaAndMCPRule(t *testing.T) {
	got := WebPreamble([]types.ToolDef{
		{
			Name:        "Read",
			Description: "Read a file",
			InputSchema: []byte(`{"type":"object","required":["file_path"],"properties":{"file_path":{"type":"string"},"offset":{"type":"number"}}}`),
		},
		{
			Name:        "mcp__github__list_prs",
			InputSchema: []byte(`{"type":"object","required":["repo"],"properties":{"repo":{"type":"string"}}}`),
		},
		{Name: "Skill", InputSchema: []byte(`{"type":"object","required":["skill"],"properties":{"skill":{"type":"string"}}}`)},
	})
	if !strings.Contains(got, "CATALOG") || !strings.Contains(got, "<tool_call>") {
		t.Fatal(got)
	}
	if !strings.Contains(got, "Read:file_path") {
		t.Fatal("read schema", got)
	}
	if !strings.Contains(got, "mcp__github__list_prs:repo") {
		t.Fatal("mcp schema", got)
	}
	if !strings.Contains(got, "Skill:skill") {
		t.Fatal("skill schema", got)
	}
	if strings.Contains(got, "Read a file") {
		t.Fatal("descriptions waste tokens")
	}
}

func TestWebCloser_ForbidsLackOfTools(t *testing.T) {
	c := WebCloser()
	if !strings.Contains(c, "<tool_call>") || !strings.Contains(c, "paste") {
		t.Fatal(c)
	}
}

func TestIsWebToolRefusal(t *testing.T) {
	if !isWebToolRefusal("Chưa đọc được repo, không mount thư mục. Gửi cho tôi README.") {
		t.Fatal("vn refusal")
	}
	if !isWebToolRefusal("I cannot access the files. Please paste README.md") {
		t.Fatal("en refusal")
	}
	if isWebToolRefusal(`{"title":"README dự án"}`) {
		t.Fatal("title is not refusal")
	}
}

func TestWrapWebStream_RefusalBecomesToolUse(t *testing.T) {
	inner := make(chan types.StreamChunk, 2)
	inner <- types.StreamChunk{Content: "Chưa đọc được repo, không mount. Gửi cho tôi README."}
	close(inner)
	out := wrapWebStream("chatgpt:01", []types.ToolDef{{Name: "Read"}, {Name: "Bash"}}, nil, inner)
	var calls []types.ToolCall
	var content string
	for ch := range out {
		content += ch.Content
		if len(ch.ToolCalls) > 0 {
			calls = ch.ToolCalls
		}
	}
	if len(calls) != 2 {
		t.Fatalf("calls=%+v", calls)
	}
	if strings.Contains(content, "Gửi cho tôi") {
		t.Fatal("refusal leaked")
	}
}

func TestFallbackExploreTools_UsesCatalogKeys(t *testing.T) {
	defs := []types.ToolDef{
		{Name: "Read", InputSchema: []byte(`{"required":["file_path"],"properties":{"file_path":{"type":"string"}}}`)},
		{Name: "Bash", InputSchema: []byte(`{"required":["command"],"properties":{"command":{"type":"string"}}}`)},
	}
	calls := fallbackExploreTools(defs)
	if len(calls) != 2 || calls[0].Name != "Read" || calls[1].Name != "Bash" {
		t.Fatalf("%+v", calls)
	}
	if !strings.Contains(calls[0].Arguments, "file_path") || !strings.Contains(calls[0].Arguments, "README.md") {
		t.Fatal(calls[0].Arguments)
	}
	if !strings.Contains(calls[1].Arguments, "git") {
		t.Fatal(calls[1].Arguments)
	}
}

func TestStripWebToolMarkup(t *testing.T) {
	in := "thinking\n```bash\nls\n```\ndone"
	got := StripWebToolMarkup(in)
	if strings.Contains(got, "ls") {
		t.Fatalf("fence left: %q", got)
	}
	if !strings.Contains(got, "thinking") {
		t.Fatalf("lost prose: %q", got)
	}
}

func TestReGitDiffAndStatusMatching(t *testing.T) {
	validDiffs := []string{
		"git diff",
		"git   diff",
		"/usr/bin/git diff",
		"run `git diff` to check",
		"\"git diff\"",
		"'git diff'",
		// bypass variants that should now match
		"git --no-pager diff",
		"git -C /some/path diff",
		"env git diff",
		"command git diff",
		"/usr/bin/git --no-pager diff",
		"/usr/local/bin/git diff",
		"/opt/homebrew/bin/git diff",
		"./git diff",
		"git -C \"path with spaces\" diff",
		"git -C 'path with spaces' diff",
		"git --git-dir=\"/repo/.git\" diff",
		"git --no-pager -C /repo diff",
	}
	for _, s := range validDiffs {
		if !reGitDiffCmd.MatchString(s) {
			t.Errorf("expected %q to match reGitDiffCmd", s)
		}
	}

	invalidDiffs := []string{
		"git diffsomething",
		"diff",
		"difference",
		"indifferent",
		"git_diff",
	}
	for _, s := range invalidDiffs {
		if reGitDiffCmd.MatchString(s) {
			t.Errorf("expected %q NOT to match reGitDiffCmd", s)
		}
	}

	validStatuses := []string{
		"git status",
		"git   status",
		"/usr/bin/git status",
		"/usr/local/bin/git status",
		"/opt/homebrew/bin/git status",
		"./git status",
		"`git status`",
		// bypass variants
		"git --no-pager status",
		"git -C /repo status",
		"git -C \"path with spaces\" status",
		"git -C 'path with spaces' status",
		"env git status",
		"command git status",
	}
	for _, s := range validStatuses {
		if !reGitStatusCmd.MatchString(s) {
			t.Errorf("expected %q to match reGitStatusCmd", s)
		}
	}

	invalidStatuses := []string{
		"status",
		"git statusupdate",
		"git_status",
	}
	for _, s := range invalidStatuses {
		if reGitStatusCmd.MatchString(s) {
			t.Errorf("expected %q NOT to match reGitStatusCmd", s)
		}
	}
}
