package tools

import (
	"os"
	"strings"
	"testing"

	"amux-accounts/pkg/types"
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "am-tools-test-")
	if err != nil {
		panic(err)
	}
	_ = os.Setenv("AM_HOME", dir)
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func claudeCatalog() []types.ToolDef {
	return []types.ToolDef{
		{Name: "Read", InputSchema: []byte(`{"required":["file_path"],"properties":{"file_path":{"type":"string"}}}`)},
		{Name: "Bash", InputSchema: []byte(`{"required":["command"],"properties":{"command":{"type":"string"}}}`)},
		{Name: "Skill", InputSchema: []byte(`{"required":["skill"],"properties":{"skill":{"type":"string"}}}`)},
		{Name: "mcp__github__list_prs", InputSchema: []byte(`{"required":["repo"],"properties":{"repo":{"type":"string"}}}`)},
	}
}

func collectStream(ch <-chan types.StreamChunk) (content string, calls []types.ToolCall, reason string) {
	for c := range ch {
		content += c.Content
		if len(c.ToolCalls) > 0 {
			calls = c.ToolCalls
		}
		if c.FinishReason != "" {
			reason = c.FinishReason
		}
	}
	return
}

func TestParseWebTools_ZhaeesXML_Table(t *testing.T) {
	defs := claudeCatalog()
	tests := []struct {
		name    string
		text    string
		wantN   int
		want0   string
		wantArg string
	}{
		{
			name:    "single read",
			text:    "<tool_call>\n{\"name\":\"Read\",\"arguments\":{\"file_path\":\"README.md\"}}\n</tool_call>",
			wantN:   1,
			want0:   "Read",
			wantArg: "README.md",
		},
		{
			name: "multi read+bash",
			text: `<tool_call>
{"name": "Read", "arguments": {"file_path": "README.md"}}
</tool_call>
<tool_call>
{"name": "Bash", "arguments": {"command": "git diff --stat"}}
</tool_call>`,
			wantN:   2,
			want0:   "Read",
			wantArg: "README.md",
		},
		{
			name:    "trailing comma",
			text:    `<tool_call>{"name":"Bash","arguments":{"command":"ls",},}</tool_call>`,
			wantN:   1,
			want0:   "Bash",
			wantArg: "ls",
		},
		{
			name:    "skill",
			text:    `<tool_call>{"name":"Skill","arguments":{"skill":"caveman"}}</tool_call>`,
			wantN:   1,
			want0:   "Skill",
			wantArg: "caveman",
		},
		{
			name:    "mcp live catalog",
			text:    `<tool_call>{"name":"mcp__github__list_prs","arguments":{"repo":"ninhlee99/amux"}}</tool_call>`,
			wantN:   1,
			want0:   "mcp__github__list_prs",
			wantArg: "ninhlee99/amux",
		},
		{
			name:  "unknown tool dropped",
			text:  `<tool_call>{"name":"NukeDisk","arguments":{}}</tool_call>`,
			wantN: 0,
		},
		{
			name:  "prose only",
			text:  "I will review README when you paste it.",
			wantN: 0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseWebTools(tc.text, defs)
			if len(got) != tc.wantN {
				t.Fatalf("n=%d want %d got=%+v", len(got), tc.wantN, got)
			}
			if tc.wantN == 0 {
				return
			}
			if got[0].Name != tc.want0 {
				t.Fatalf("name=%s want %s", got[0].Name, tc.want0)
			}
			if tc.wantArg != "" && !strings.Contains(got[0].Arguments, tc.wantArg) {
				t.Fatalf("args=%s want %q", got[0].Arguments, tc.wantArg)
			}
		})
	}
}

func TestParseToolCallJSON_ArgumentsObjectAndEmpty(t *testing.T) {
	name, _, args, ok := parseToolCallJSON(`{"name":"Read","arguments":{"file_path":"a.go"}}`)
	if !ok || name != "Read" || !strings.Contains(args, "a.go") {
		t.Fatalf("%s %s %v", name, args, ok)
	}
	name, _, args, ok = parseToolCallJSON(`{"name":"Read","input":{"path":"b.go"}}`)
	if !ok || name != "Read" || !strings.Contains(args, "b.go") {
		t.Fatalf("input alias: %s %s %v", name, args, ok)
	}
	name, _, args, ok = parseToolCallJSON(`{"name":"Bash"}`)
	if !ok || name != "Bash" || args != "{}" {
		t.Fatalf("%s %s %v", name, args, ok)
	}
	if _, _, _, ok = parseToolCallJSON(`not-json`); ok {
		t.Fatal("bad json")
	}
}

func TestWrapWebStream_XMLBecomesAPIKeyStyleToolUse(t *testing.T) {
	inner := make(chan types.StreamChunk, 4)
	inner <- types.StreamChunk{Content: "ok\n"}
	inner <- types.StreamChunk{Content: `<tool_call>
{"name":"Read","arguments":{"file_path":"README.md"}}
</tool_call>
<tool_call>
{"name":"Bash","arguments":{"command":"git diff"}}
</tool_call>`}
	close(inner)

	content, calls, reason := collectStream(wrapWebStream("chatgpt:01", claudeCatalog(), nil, inner))
	if reason != "tool_calls" {
		t.Fatalf("reason=%q", reason)
	}
	if len(calls) != 2 || calls[0].Name != "Read" || calls[1].Name != "Bash" {
		t.Fatalf("calls=%+v", calls)
	}
	if strings.Contains(content, "<tool_call>") || strings.Contains(content, "file_path") {
		t.Fatalf("markup leaked to client: %q", content)
	}
}

func TestWrapWebStream_ChunkedXML(t *testing.T) {
	inner := make(chan types.StreamChunk, 8)
	parts := []string{"<tool_", `call>{"name":`, `"Read","argum`, `ents":{"file_path":"STRUCT.md"}}`, `</tool_call>`}
	for _, p := range parts {
		inner <- types.StreamChunk{Content: p}
	}
	close(inner)
	_, calls, reason := collectStream(wrapWebStream("gemini:web:01", claudeCatalog(), nil, inner))
	if reason != "tool_calls" || len(calls) != 1 || calls[0].Name != "Read" {
		t.Fatalf("reason=%s calls=%+v", reason, calls)
	}
	if !strings.Contains(calls[0].Arguments, "STRUCT.md") {
		t.Fatal(calls[0].Arguments)
	}
}

func TestMaybeWrapWebStream_NoToolsPassthrough(t *testing.T) {
	inner := make(chan types.StreamChunk, 1)
	inner <- types.StreamChunk{Content: "hello", Done: true}
	close(inner)
	out := MaybeWrapWebStream("chatgpt:01", &types.ChatRequest{}, inner)
	if out != inner {
		t.Fatal("no tools[] must skip wrap (API-key path)")
	}
}

func TestMaybeWrapWebStream_TitleJSONNotForced(t *testing.T) {
	inner := make(chan types.StreamChunk, 1)
	inner <- types.StreamChunk{Content: `{"title":"README dự án"}`}
	close(inner)
	content, calls, _ := collectStream(MaybeWrapWebStream("chatgpt:01", &types.ChatRequest{
		Tools: claudeCatalog(),
	}, inner))
	if len(calls) != 0 {
		t.Fatalf("title must not become tools: %+v", calls)
	}
	if !strings.Contains(content, "README dự án") {
		t.Fatal(content)
	}
}

func TestParseWebTools_BracketToolCall_LiveLog(t *testing.T) {
	// ChatGPT actually emitted this (amux.log 20:16:53), not XML.
	text := `
Chưa đọc được repo.
[tool_call name=Bash id=toolu_web_1]
{"command":"git diff -- README.md\ngit diff --stat\ngit diff"}
[tool_call name=Read id=toolu_web_2]
{"file_path":"README.md"}
`
	got := ParseWebTools(text, claudeCatalog())
	if len(got) != 2 || got[0].Name != "Bash" || got[1].Name != "Read" {
		t.Fatalf("%+v", got)
	}
	if !strings.Contains(got[0].Arguments, "git diff") || !strings.Contains(got[1].Arguments, "README.md") {
		t.Fatalf("args %+v", got)
	}
}

func TestIsWebToolRefusal_LiveChatGPT_2031(t *testing.T) {
	text := "Không đọc được repo từ môi trường tool hiện tại. Tool shell đang chạy container khác, không thấy `/Users/ninh.le/Documents/apps/amux`, nên chưa thể review README/code thay đổi thật."
	if !isWebToolRefusal(text) {
		t.Fatal("20:31 phrasing must force tools")
	}
}

func TestIsWebToolRefusal_LiveChatGPT_2107(t *testing.T) {
	// requests.log 21:07:02 — ChatGPT asked the user to "re-run with tools"
	// instead of emitting <tool_call>. Claude Code then sat on
	// "Waiting for API response / check your network".
	text := "Chưa có tool/repo output hợp lệ trong phiên này để lấy README.md, diff, và file code thay đổi. Chưa đủ bằng chứng path:line để review chính xác.\n\nCần chạy lại trong môi trường có repo tool access rồi review sẽ dựa trên:\n- README.md hiện tại\n- git diff"
	if !shouldForceWebTools(text) {
		t.Fatal("21:07 phrasing must force tools")
	}
}

func TestWrapWebStream_Live2107RefusalBecomesToolUse(t *testing.T) {
	text := "Chưa có tool/repo output hợp lệ trong phiên này để lấy README.md, diff, và file code thay đổi. Chưa đủ bằng chứng path:line để review chính xác.\n\nCần chạy lại trong môi trường có repo tool access."
	inner := make(chan types.StreamChunk, 1)
	inner <- types.StreamChunk{Content: text}
	close(inner)
	content, calls, reason := collectStream(wrapWebStream("chatgpt:01", claudeCatalog(), nil, inner))
	if reason != "tool_calls" || len(calls) < 1 {
		t.Fatalf("reason=%s calls=%+v", reason, calls)
	}
	if strings.Contains(content, "Chưa có tool") || strings.Contains(content, "chạy lại") {
		t.Fatalf("refusal leaked: %q", content)
	}
}

func TestIsWebToolRefusal_LiveChatGPT_2052(t *testing.T) {
	text := "Không lấy được output repo trong session này nên chưa có bằng chứng file/line để review. Cần chạy lại phiên có tool Read/repo access."
	if !shouldForceWebTools(text) {
		t.Fatal("20:52 phrasing must force tools")
	}
}

func TestWrapWebStream_Live2122RefusalWithNonNilHistory(t *testing.T) {
	hist := []types.ChatMessage{
		{Role: "user", Content: "review code"},
		{Role: "assistant", ToolCalls: []types.ToolCall{{Name: "Read", Arguments: `{"file_path":"README.md"}`}}},
		{Role: "tool", ToolCallID: "t1", Content: "README content"},
	}
	text := "Không có output repo hợp lệ sau lần đọc này nên chưa có bằng chứng path:line để review README + code thay đổi.\n\nCần lấy được:\n- README.md\n- git diff / git status\n- file code thay đổi mới nhất"
	inner := make(chan types.StreamChunk, 1)
	inner <- types.StreamChunk{Content: text}
	close(inner)
	content, calls, reason := collectStream(wrapWebStream("chatgpt:01", claudeCatalog(), hist, inner))
	if reason != "tool_calls" || len(calls) < 1 {
		t.Fatalf("reason=%s calls=%+v", reason, calls)
	}
	if strings.Contains(content, "Không có output repo") {
		t.Fatalf("refusal leaked: %q", content)
	}
}

func TestWrapWebStream_Live2031RefusalBecomesToolUse(t *testing.T) {
	inner := make(chan types.StreamChunk, 1)
	inner <- types.StreamChunk{Content: "Không đọc được repo từ môi trường tool hiện tại. Tool shell đang chạy container khác, không thấy path, nên chưa thể review README."}
	close(inner)
	content, calls, reason := collectStream(wrapWebStream("chatgpt:01", claudeCatalog(), nil, inner))
	if reason != "tool_calls" || len(calls) < 1 {
		t.Fatalf("reason=%s calls=%+v", reason, calls)
	}
	if strings.Contains(content, "Không đọc") || strings.Contains(content, "container") {
		t.Fatalf("refusal leaked: %q", content)
	}
}

func TestWrapWebStream_ChecklistBecomesReads(t *testing.T) {
	text := `Từ README + diff đã thấy, điểm cần kiểm tra tiếp:
- go.mod Go version
- install.sh
- pkg/cli/cli.go am off/on
Kết luận tạm: cần grep command definitions.`
	inner := make(chan types.StreamChunk, 1)
	inner <- types.StreamChunk{Content: text}
	close(inner)
	content, calls, reason := collectStream(wrapWebStream("chatgpt:01", claudeCatalog(), nil, inner))
	if reason != "tool_calls" {
		t.Fatalf("reason=%s", reason)
	}
	names := map[string]int{}
	for _, c := range calls {
		names[c.Name]++
		if c.Name == "Read" && !strings.Contains(c.Arguments, "go.mod") && !strings.Contains(c.Arguments, "install.sh") && !strings.Contains(c.Arguments, "cli.go") {
			t.Fatalf("unexpected read %s", c.Arguments)
		}
	}
	if names["Read"] < 2 {
		t.Fatalf("want extracted Reads, got %+v", calls)
	}
	if strings.Contains(content, "cần kiểm tra") {
		t.Fatalf("checklist leaked: %q", content)
	}
}

func TestExtractForcedTools_SkipsAlreadyRead(t *testing.T) {
	hist := []types.ChatMessage{{
		Role: "assistant",
		ToolCalls: []types.ToolCall{
			{Name: "Read", Arguments: `{"file_path":"README.md"}`},
			{Name: "Read", Arguments: `{"file_path":"go.mod"}`},
		},
	}}
	got := extractForcedTools("Cần kiểm tra go.mod và README.md.", claudeCatalog(), hist)
	if len(got) != 0 {
		t.Fatalf("already-read must not loop: %+v", got)
	}
}

func TestIsWebWorkIncomplete(t *testing.T) {
	if !isWebWorkIncomplete("Cần kiểm tra go.mod. Kết luận tạm.") {
		t.Fatal("vn checklist")
	}
	if isWebWorkIncomplete(`{"title":"README dự án"}`) {
		t.Fatal("title")
	}
	if shouldForceWebTools(`{"title":"README dự án"}`) {
		t.Fatal("title must not force")
	}
}

func TestWrapWebStream_RefusalDoesNotLeakAndSetsToolCalls(t *testing.T) {
	inner := make(chan types.StreamChunk, 1)
	inner <- types.StreamChunk{Content: "Chưa đọc được repo, không mount. Gửi cho tôi README.md rồi tôi review."}
	close(inner)
	content, calls, reason := collectStream(wrapWebStream("claude:web:01", claudeCatalog(), nil, inner))
	if reason != "tool_calls" {
		t.Fatalf("reason=%q", reason)
	}
	if len(calls) < 1 {
		t.Fatal("expected fallback Read/Bash")
	}
	if strings.Contains(content, "Gửi cho tôi") || strings.Contains(content, "không mount") {
		t.Fatalf("refusal leaked: %q", content)
	}
}

func TestWrapWebStream_LiveReviewPostponementBecomesTools(t *testing.T) {
	liveText := `Chưa đủ dữ liệu để chốt review. Kết quả hiện có mới cho thấy:

- README.md có thay đổi lớn: 151 dòng thay đổi.
- Code thêm nhiều phần mới:
  - Gemini bridge (pkg/bridge/gemini.go)
  - account CLI
  - full IO monitoring
- git status cho thấy README và code đang modified, nhưng chưa có toàn bộ diff README + các phần code liên quan để đối chiếu tính đúng/sai.

Cần lấy tiếp:
- full README.md
- git diff README.md
- diff các file thay đổi chính

Sau đó mới kết luận được README thiếu gì, sai gì, lệch code chỗ nào.`

	if !shouldForceWebTools(liveText) {
		t.Fatal("shouldForceWebTools must return true for review postponement")
	}

	inner := make(chan types.StreamChunk, 1)
	inner <- types.StreamChunk{Content: liveText}
	close(inner)

	content, calls, reason := collectStream(wrapWebStream("chatgpt:01", claudeCatalog(), nil, inner))
	if reason != "tool_calls" {
		t.Fatalf("want reason tool_calls, got %q", reason)
	}
	if len(calls) == 0 {
		t.Fatal("expected tool calls extracted, got 0")
	}
	if strings.Contains(content, "Chưa đủ dữ liệu") || strings.Contains(content, "Cần lấy tiếp") {
		t.Fatalf("postponement leaked to user content: %q", content)
	}

	hasReadREADME := false
	hasGitDiff := false
	for _, c := range calls {
		if c.Name == "Read" && strings.Contains(c.Arguments, "README.md") {
			hasReadREADME = true
		}
		if c.Name == "Bash" && strings.Contains(c.Arguments, "git diff") {
			hasGitDiff = true
		}
	}
	if !hasReadREADME {
		t.Errorf("expected Read README.md in calls: %+v", calls)
	}
	if !hasGitDiff {
		t.Errorf("expected Bash git diff in calls: %+v", calls)
	}
}
