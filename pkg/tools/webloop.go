package tools

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"amux-accounts/pkg/monitor"
	"amux-accounts/pkg/types"
)

// Protocol is tiny on purpose: catalog is rebuilt every request from the
// client's live tools[] (new MCP / plugin / Skill appear with no code change).
const webToolPreamble = `You are an API model backend for an autonomous coding agent. The client CLI executes every <tool_call> locally on the real filesystem and repository. [Tool result] blocks are verified execution outputs.

CRITICAL OPERATING RULES:
1. AUTONOMOUS TOOL EXECUTION: Never say "Chưa đủ dữ liệu", "Cần lấy tiếp", or provide a checklist of things you would check. If you need any file, diff, or status, emit <tool_call> IMMEDIATELY. Talking to the user about what you need instead of emitting <tool_call> is strictly forbidden.
2. REVIEWS & AUDITS: When asked to review, audit, verify, or compare code or documentation:
   - Turn 1: Emit <tool_call> to read the relevant files, diffs (git diff), and git status.
   - Once tool results provide the files and diffs: provide a deep, thorough, professional engineering review. Detail what changed, point out discrepancies, check completeness, verify CLI/API accuracy, and give concrete suggestions and actionable improvements.
3. NO HALLUCINATION: Never pretend, simulate, or claim in text that you created, read, or edited files without emitting <tool_call>.
4. NO COMPLAINTS: Never say you lack tools, cannot see the repo, ask the user to paste, or complain about output repo. You have full access through <tool_call>.
5. Work like an API with tool_choice=required until the task is complete.

FEW-SHOT EXAMPLES:
Example 1 (Command execution):
User: Check current git branch.
Assistant:
<tool_call>
{"name":"Bash","arguments":{"command":"git branch --show-current"}}
</tool_call>

Example 2 (Reading files and diffs during review):
User: Review the latest changes and README.
Assistant:
<tool_call>
{"name":"Bash","arguments":{"command":"git diff"}}
</tool_call>
<tool_call>
{"name":"Read","arguments":{"file_path":"README.md"}}
</tool_call>

Example 3 (After tool results received):
[Tool result — CLI ran]:
<git diff content and README content>
Assistant:
# Code & Documentation Review
[Thorough, detailed analysis with specific line references, findings, and suggestions]

ONLY <tool_call> blocks when using tools. Multiple = multiple blocks. CATALOG
`

const webToolCloser = `
[end] MANDATORY TOOL INSTRUCTION: You have active local access to tools (Bash, Read, Grep, Glob, Agent, Edit, Write). If you need any file content, grep search, or command execution, output <tool_call> now. NEVER say "Không có tool", "Chưa đủ dữ liệu", or "Cần output đọc file" — emit the <tool_call> immediately. Once tool results provide the required data, provide a complete, in-depth answer directly. No checklist. No paste.
`

var (
	reXMLTool      = regexp.MustCompile(`(?s)<tool_call>\s*(.*?)\s*</tool_call>`)
	reAMUXTool     = regexp.MustCompile(`(?s)<<<AMUX_TOOL\s+name="([^"]+)"(?:\s+id="([^"]*)")?\s*>>>\s*(.*?)\s*<<<END_AMUX_TOOL>>>`)
	reToolJSON     = regexp.MustCompile("(?s)```(?:tool_call|json)\\s*\n(\\{[\\s\\S]*?\\})\\s*```")
	reBashFence    = regexp.MustCompile("(?s)```(?:bash|sh|zsh|shell)\\s*\n(.*?)\\s*```")
	// ChatGPT copies Claude Code's display form: [tool_call name=Bash id=…]
	reBracketTool  = regexp.MustCompile(`(?s)\[tool_call\s+name="?([^"\s\]]+)"?(?:\s+id="?([^"\s\]]+)"?)?\]\s*(\{.*?\})`)
	reTrailComma   = regexp.MustCompile(`,\s*([}\]])`)
	reWebFilePath  = regexp.MustCompile(`(?i)\b(?:[\w.-]+/)*[\w.-]+\.(?:go|md|sh|json|mod|sum|yml|yaml|toml|txt|html|ts|js|py|rs|c|cpp|h|hpp|sql)\b`)
	reWebBashCmd   = regexp.MustCompile("(?i)(?:`((?:git|ls|find|grep|am)\\s+[^`\\n]+)`|\\b(git\\s+(?:diff|status|log|show|branch|grep|rev-parse)(?:\\s+(?:--?[a-zA-Z0-9_.-]+|[a-zA-Z0-9_./-]+))*|ls(?:\\s+-[a-zA-Z0-9]+)?)\\b)")
	// reGitDiffCmd matches git diff in various real-world forms for webloop tool extraction:
	//   git diff, git --no-pager diff, git -C /path diff, git -C "my path" diff
	//   /usr/bin/git, /usr/local/bin/git, /opt/homebrew/bin/git, env git, command git
	// NOTE: This is a friendly heuristic extractor for web backend outputs, not a security boundary.
	reGitDiffCmd = regexp.MustCompile(
		`(?i)(?:^|[^a-z0-9_/-])(?:(?:env|command)\s+)?(?:\S+/)?git(?:\s+(?:--?\S+(?:="[^"]*"|='[^']*'|=\S+)?|-C\s+(?:"[^"]*"|'[^']*'|\S+)))*\s+diff\b`,
	)
	// reGitStatusCmd mirrors reGitDiffCmd but for git status.
	reGitStatusCmd = regexp.MustCompile(
		`(?i)(?:^|[^a-z0-9_/-])(?:(?:env|command)\s+)?(?:\S+/)?git(?:\s+(?:--?\S+(?:="[^"]*"|='[^']*'|=\S+)?|-C\s+(?:"[^"]*"|'[^']*'|\S+)))*\s+status\b`,
	)
	reWebTitleJSON = regexp.MustCompile(`(?s)^\s*\{\s*"title"\s*:`)
)

// WebPreamble is appended to a web-backend prompt when the client sent tools[].
func WebPreamble(defs []types.ToolDef) string {
	if len(defs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(webToolPreamble)
	for _, d := range defs {
		b.WriteString(catalogLine(d))
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	return b.String()
}

// catalogLine is "Name" or "Name:req1,req2" — live tools[], no hardcoded list.
func catalogLine(d types.ToolDef) string {
	keys := schemaKeys(d.InputSchema, 4)
	if len(keys) == 0 {
		return d.Name
	}
	return d.Name + ":" + strings.Join(keys, ",")
}

func schemaKeys(raw json.RawMessage, max int) []string {
	if len(raw) == 0 || string(raw) == "null" || max < 1 {
		return nil
	}
	var s struct {
		Required   []string                   `json:"required"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if json.Unmarshal(raw, &s) != nil {
		return nil
	}
	if len(s.Required) > 0 {
		if len(s.Required) > max {
			return s.Required[:max]
		}
		return s.Required
	}
	keys := make([]string, 0, len(s.Properties))
	for k := range s.Properties {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) > max {
		keys = keys[:max]
	}
	return keys
}

// WebCloser is appended after the flattened transcript so it outranks
// Claude Code's native tool-harness instructions.
func WebCloser() string {
	return webToolCloser
}

// MaybeWrapWebStream parses a text-only web stream into ToolCalls when the
// client sent tools[]. Logs extracted tools, then forwards them so Claude
// Code / Cursor can execute locally.
func MaybeWrapWebStream(source string, req *types.ChatRequest, inner <-chan types.StreamChunk) <-chan types.StreamChunk {
	if inner == nil || req == nil || len(req.Tools) == 0 {
		return inner
	}
	return wrapWebStream(source, req.Tools, req.Messages, inner)
}

func wrapWebStream(source string, defs []types.ToolDef, hist []types.ChatMessage, inner <-chan types.StreamChunk) <-chan types.StreamChunk {
	out := make(chan types.StreamChunk, 8)
	go func() {
		defer close(out)
		var buf strings.Builder
		id := source
		for ch := range inner {
			if ch.ID != "" {
				id = ch.ID
			}
			if ch.Error != nil {
				out <- ch
				return
			}
			if len(ch.ToolCalls) > 0 {
				raw := buf.String()
				if ch.LogText == "" {
					ch.LogText = raw
				}
				logWebTools(source, ch.ToolCalls, ch.LogText)
				out <- ch
				for rest := range inner {
					out <- rest
				}
				return
			}
			buf.WriteString(ch.Content)
		}
		text := buf.String()
		calls := ParseWebTools(text, defs)
		forced := false
		if shouldForceWebTools(text, hist) {
			extra := extractForcedTools(text, defs, hist)
			for _, tc := range extra {
				if !hasToolCall(calls, tc) {
					calls = append(calls, tc)
				}
			}
			forced = len(calls) > 0
		}
		logWebTools(source, calls, text)
		if len(calls) == 0 {
			if text != "" {
				out <- types.StreamChunk{ID: id, Content: text, LogText: text}
			}
			out <- types.StreamChunk{ID: id, Done: true, LogText: text}
			return
		}
		// API-key style: tool_use only. Drop "please paste" prose.
		if !forced {
			if visible := StripWebToolMarkup(text); strings.TrimSpace(visible) != "" {
				out <- types.StreamChunk{ID: id, Content: visible, LogText: text}
			}
		}
		out <- types.StreamChunk{
			ID:           id,
			ToolCalls:    calls,
			FinishReason: "tool_calls",
			Done:         true,
			LogText:      text,
		}
	}()
	return out
}

// FormatToolCalls is a one-line preview: `Bash {"command":"ls"} · Read {"path":"a"}`.
func FormatToolCalls(calls []types.ToolCall) string {
	if len(calls) == 0 {
		return ""
	}
	parts := make([]string, 0, len(calls))
	for _, c := range calls {
		arg := strings.TrimSpace(c.Arguments)
		if len(arg) > 120 {
			arg = arg[:120] + "…"
		}
		if arg == "" {
			parts = append(parts, c.Name)
			continue
		}
		parts = append(parts, c.Name+" "+arg)
	}
	return strings.Join(parts, " · ")
}

func isWebToolRefusal(text string) bool {
	low := strings.ToLower(text)
	needles := []string{
		"cannot access", "can't access", "do not have access", "don't have access",
		"cannot read", "can't read", "no access to", "not mounted",
		"paste", "upload repo", "upload the", "send me the",
		"run on your", "run it locally", "on your machine",
		"chưa đọc", "không đọc", "không thấy", "không thể", "chưa thể",
		"không mount", "không truy cập", "gửi cho tôi",
		"gửi file", "chạy trong máy", "máy bạn",
		"môi trường tool", "container khác", "cannot see", "can't see",
		"unable to access", "unable to read", "unable to see", "unable to review",
		// live ChatGPT refusals (requests.log 20:52 / 21:07 / 21:22 / 22:27): model asks the
		// user to re-run with tools instead of emitting <tool_call>.
		"chưa có tool", "không lấy được", "chưa đủ bằng chứng", "chưa có bằng chứng",
		"không có quyền", "không cấp quyền", "tool access", "repo access",
		"cần chạy lại", "output repo", "không có kết quả",
		"không có output", "sau lần đọc này", "cần lấy được", "không chạy giả tool",
		"chưa có repo tool", "đọc thêm code sau", "không có read", "cần phiên có",
		"không có bằng chứng", "sẽ review đối chiếu", "không có tool",
		"không khả dụng", "trong phiên này", "khả dụng trong", "không có write",
		"không có bash", "không hỗ trợ tool", "chưa hỗ trợ tool",
	}
	for _, n := range needles {
		if strings.Contains(low, n) {
			return true
		}
	}
	return false
}

func isWebTitleJSON(text string) bool {
	s := strings.TrimSpace(text)
	if !reWebTitleJSON.MatchString(s) {
		return false
	}
	var v struct {
		Title string `json:"title"`
	}
	return json.Unmarshal([]byte(s), &v) == nil && strings.TrimSpace(v.Title) != ""
}

func isWebWorkIncomplete(text string) bool {
	low := strings.ToLower(text)
	needles := []string{
		"cần kiểm tra", "cần grep", "cần đọc", "cần đối chiếu", "cần review",
		"cần xác nhận", "cần test", "điểm cần", "kết luận tạm",
		"không có tool", "không có read", "mở lại phiên", "cấp tool",
		"chưa có tool", "cần chạy lại", "repo tool",
		"need to check", "need to verify", "need to read", "need to grep",
		"cannot complete", "unable to complete",
		"cần lấy được", "chưa có bằng chứng", "cần phiên có",
		// Live postponement and refusal patterns:
		"chưa đủ dữ liệu", "cần lấy tiếp", "chưa có toàn bộ", "chưa có diff",
		"sau đó mới kết luận", "sau đó mới", "chưa đủ thông tin", "cần lấy thêm",
		"chưa thể kết luận", "chưa thể chốt", "cần thêm dữ liệu", "cần có dữ liệu",
		"not enough data", "need to fetch", "need more data", "further inspection",
	}
	for _, n := range needles {
		if strings.Contains(low, n) {
			return true
		}
	}
	return false
}

func isWebFakeExecution(text string, hist []types.ChatMessage) bool {
	low := strings.ToLower(text)
	fakePhrases := []string{
		"tạo file", "đã tạo", "đọc lại thành công", "đã ghi", "đã sửa", "đã chạy",
		"i have created", "i created", "i have written", "file has been created",
		"xong. đọc lại", "đã tạo file",
	}
	hasFakePhrase := false
	for _, p := range fakePhrases {
		if strings.Contains(low, p) {
			hasFakePhrase = true
			break
		}
	}
	if !hasFakePhrase {
		return false
	}
	lastRole := ""
	if len(hist) > 0 {
		lastRole = strings.ToLower(hist[len(hist)-1].Role)
	}
	return lastRole != "tool"
}

func shouldForceWebTools(text string, hist ...[]types.ChatMessage) bool {
	if strings.TrimSpace(text) == "" || isWebTitleJSON(text) {
		return false
	}
	var h []types.ChatMessage
	if len(hist) > 0 {
		h = hist[0]
	}
	return isWebToolRefusal(text) || isWebWorkIncomplete(text) || isWebFakeExecution(text, h)
}

func filesFromHistory(hist []types.ChatMessage) map[string]bool {
	seen := map[string]bool{}
	for _, m := range hist {
		for _, tc := range m.ToolCalls {
			if !strings.EqualFold(tc.Name, "Read") {
				continue
			}
			var args map[string]any
			if json.Unmarshal([]byte(tc.Arguments), &args) != nil {
				continue
			}
			for _, k := range []string{"file_path", "path"} {
				if p, ok := args[k].(string); ok && p != "" {
					seen[p] = true
				}
			}
		}
	}
	return seen
}

func historyHasTools(hist []types.ChatMessage) bool {
	for _, m := range hist {
		if strings.EqualFold(m.Role, "tool") || len(m.ToolCalls) > 0 {
			return true
		}
	}
	return false
}

func extractForcedTools(text string, defs []types.ToolDef, hist []types.ChatMessage) []types.ToolCall {
	by := map[string]types.ToolDef{}
	for _, d := range defs {
		by[strings.ToLower(d.Name)] = d
	}
	already := filesFromHistory(hist)
	var out []types.ToolCall

	addRead := func(path string) {
		d, ok := by["read"]
		if !ok || path == "" || already[path] {
			return
		}
		already[path] = true
		key := toolArgKey(d, "file_path", "path")
		b, _ := json.Marshal(map[string]string{key: path})
		out = append(out, types.ToolCall{
			ID:        fmt.Sprintf("toolu_web_ex_%d", len(out)+1),
			Name:      d.Name,
			Arguments: string(b),
		})
	}

	addBash := func(cmd string) {
		d, ok := by["bash"]
		if !ok || strings.TrimSpace(cmd) == "" {
			return
		}
		key := toolArgKey(d, "command")
		b, _ := json.Marshal(map[string]string{key: strings.TrimSpace(cmd)})
		out = append(out, types.ToolCall{
			ID:        fmt.Sprintf("toolu_web_ex_bash_%d", len(out)+1),
			Name:      d.Name,
			Arguments: string(b),
		})
	}

	searchText := text
	if isWebToolRefusal(text) || isWebWorkIncomplete(text) {
		for i := len(hist) - 1; i >= 0; i-- {
			if strings.EqualFold(hist[i].Role, "user") {
				searchText = text + "\n" + hist[i].Content
				break
			}
		}
	}
	// 1. Shell/Git commands mentioned in text (in backticks or git invocations)
	for _, m := range reWebBashCmd.FindAllStringSubmatch(searchText, 8) {
		cmd := ""
		for idx := 1; idx < len(m); idx++ {
			if m[idx] != "" {
				cmd = strings.Trim(m[idx], "` ")
				break
			}
		}
		if cmd != "" && !hasBashCommand(out, cmd) {
			addBash(cmd)
		}
		if len(out) >= 8 {
			break
		}
	}

	// 2. File paths mentioned in text or user prompt
	for _, p := range reWebFilePath.FindAllString(searchText, 12) {
		if strings.HasPrefix(strings.ToLower(p), "http") {
			continue
		}
		parts := strings.Split(p, "/")
		extParts := 0
		for _, part := range parts {
			if strings.Contains(part, ".") {
				extParts++
			}
		}
		if extParts > 1 {
			for _, part := range parts {
				if strings.Contains(part, ".") {
					addRead(part)
				}
			}
			continue
		}
		addRead(p)
		if len(out) >= 6 {
			break
		}
	}

	if _, hasBash := by["bash"]; hasBash {
		if reGitDiffCmd.MatchString(searchText) && !hasBashCommand(out, "git diff") {
			addBash("git diff --stat && git diff")
		}
		if reGitStatusCmd.MatchString(searchText) && !hasBashCommand(out, "git status") {
			addBash("git status -sb")
		}
	}

	// 3. Fallback when 0 tools extracted:
	if len(out) == 0 {
		if isWebToolRefusal(text) {
			return fallbackExploreTools(defs)
		}
		return nil
	}

	if d, ok := by["bash"]; ok && !historyHasTools(hist) && !hasBashCommand(out, "") {
		key := toolArgKey(d, "command")
		b, _ := json.Marshal(map[string]string{key: "git status -sb && git diff --stat && git diff -- README.md"})
		out = append(out, types.ToolCall{ID: "toolu_web_ex_bash", Name: d.Name, Arguments: string(b)})
	}
	return out
}

func hasBashCommand(calls []types.ToolCall, sub string) bool {
	for _, c := range calls {
		if strings.EqualFold(c.Name, "bash") {
			if sub == "" || strings.Contains(c.Arguments, sub) {
				return true
			}
		}
	}
	return false
}

func hasToolCall(calls []types.ToolCall, tc types.ToolCall) bool {
	for _, c := range calls {
		if strings.EqualFold(c.Name, tc.Name) && c.Arguments == tc.Arguments {
			return true
		}
		if strings.EqualFold(c.Name, "bash") && strings.EqualFold(tc.Name, "bash") {
			var a1, a2 map[string]string
			_ = json.Unmarshal([]byte(c.Arguments), &a1)
			_ = json.Unmarshal([]byte(tc.Arguments), &a2)
			if a1["command"] != "" && a1["command"] == a2["command"] {
				return true
			}
		}
		if strings.EqualFold(c.Name, "read") && strings.EqualFold(tc.Name, "read") {
			var a1, a2 map[string]string
			_ = json.Unmarshal([]byte(c.Arguments), &a1)
			_ = json.Unmarshal([]byte(tc.Arguments), &a2)
			p1 := a1["file_path"]
			if p1 == "" {
				p1 = a1["path"]
			}
			p2 := a2["file_path"]
			if p2 == "" {
				p2 = a2["path"]
			}
			if p1 != "" && p1 == p2 {
				return true
			}
		}
	}
	return false
}

// fallbackExploreTools emits the same first moves an API-key model would:
// Read + Bash from the live catalog so Claude Code executes locally.
func fallbackExploreTools(defs []types.ToolDef) []types.ToolCall {
	by := map[string]types.ToolDef{}
	for _, d := range defs {
		by[strings.ToLower(d.Name)] = d
	}
	var out []types.ToolCall
	if d, ok := by["read"]; ok {
		key := toolArgKey(d, "file_path", "path")
		b, _ := json.Marshal(map[string]string{key: "README.md"})
		out = append(out, types.ToolCall{ID: "toolu_web_fb_1", Name: d.Name, Arguments: string(b)})
	}
	if d, ok := by["bash"]; ok {
		key := toolArgKey(d, "command")
		b, _ := json.Marshal(map[string]string{key: "git status -sb && git diff --stat && git diff -- README.md"})
		out = append(out, types.ToolCall{ID: "toolu_web_fb_2", Name: d.Name, Arguments: string(b)})
	}
	return out
}

func toolArgKey(d types.ToolDef, prefer ...string) string {
	keys := schemaKeys(d.InputSchema, 8)
	for _, p := range prefer {
		for _, k := range keys {
			if k == p {
				return p
			}
		}
	}
	if len(keys) > 0 {
		return keys[0]
	}
	if len(prefer) > 0 {
		return prefer[0]
	}
	return "input"
}

func logWebTools(source string, calls []types.ToolCall, raw string) {
	n := len([]rune(strings.TrimSpace(raw)))
	if len(calls) > 0 {
		monitor.AppendEvent("TOOLS", fmt.Sprintf("%s → claude %s · %d chars", source, FormatToolCalls(calls), n))
		return
	}
	if n == 0 {
		monitor.AppendEvent("TOOLS", source+" no tool_use")
		return
	}
	monitor.AppendEvent("TOOLS", fmt.Sprintf("%s no tool_use · %d chars (see REPLY)", source, n))
}

// ParseWebTools extracts tool calls from a web model's text reply.
func ParseWebTools(text string, defs []types.ToolDef) []types.ToolCall {
	allow := map[string]string{} // lower → canonical
	for _, d := range defs {
		if d.Name != "" {
			allow[strings.ToLower(d.Name)] = d.Name
		}
	}
	canonical := func(name string) (string, bool) {
		if len(allow) == 0 {
			return name, name != ""
		}
		c, ok := allow[strings.ToLower(name)]
		return c, ok
	}

	var out []types.ToolCall
	seen := map[string]bool{}
	add := func(name, id, args string) {
		canon, ok := canonical(name)
		if !ok {
			return
		}
		args = strings.TrimSpace(args)
		if args == "" {
			args = "{}"
		}
		if !json.Valid([]byte(args)) {
			// treat as shell command / raw string
			b, _ := json.Marshal(map[string]string{"command": args})
			args = string(b)
		}
		if id == "" {
			id = fmt.Sprintf("toolu_web_%d", len(out)+1)
		}
		key := canon + "\n" + args
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, types.ToolCall{ID: id, Name: canon, Arguments: args})
	}

	for _, m := range reXMLTool.FindAllStringSubmatch(text, -1) {
		if name, id, args, ok := parseToolCallJSON(m[1]); ok {
			add(name, id, args)
		}
	}
	for _, m := range reBracketTool.FindAllStringSubmatch(text, -1) {
		name, id, raw := m[1], m[2], strings.TrimSpace(m[3])
		if n, i, args, ok := parseToolCallJSON(raw); ok {
			if n != "" {
				name = n
			}
			if i != "" {
				id = i
			}
			add(name, id, args)
			continue
		}
		add(name, id, raw)
	}
	for _, m := range reAMUXTool.FindAllStringSubmatch(text, -1) {
		add(m[1], m[2], m[3])
	}
	for _, m := range reToolJSON.FindAllStringSubmatch(text, -1) {
		var probe struct {
			Name      string          `json:"name"`
			ID        string          `json:"id"`
			Arguments json.RawMessage `json:"arguments"`
			Input     json.RawMessage `json:"input"`
		}
		if json.Unmarshal([]byte(m[1]), &probe) != nil || probe.Name == "" {
			continue
		}
		args := probe.Arguments
		if len(args) == 0 {
			args = probe.Input
		}
		add(probe.Name, probe.ID, string(args))
	}
	if _, bashOK := canonical("Bash"); bashOK || len(allow) == 0 {
		for _, m := range reBashFence.FindAllStringSubmatch(text, -1) {
			cmd := strings.TrimSpace(m[1])
			if cmd == "" {
				continue
			}
			b, _ := json.Marshal(map[string]string{"command": cmd})
			add("Bash", "", string(b))
		}
	}
	return out
}

func parseToolCallJSON(raw string) (name, id, args string, ok bool) {
	raw = strings.TrimSpace(raw)
	raw = reTrailComma.ReplaceAllString(raw, "$1")
	var probe struct {
		Name      string          `json:"name"`
		ID        string          `json:"id"`
		Arguments json.RawMessage `json:"arguments"`
		Input     json.RawMessage `json:"input"`
	}
	if json.Unmarshal([]byte(raw), &probe) != nil || probe.Name == "" {
		return "", "", "", false
	}
	a := probe.Arguments
	if len(a) == 0 {
		a = probe.Input
	}
	if len(a) == 0 {
		return probe.Name, probe.ID, "{}", true
	}
	return probe.Name, probe.ID, string(a), true
}

// StripWebToolMarkup removes protocol / bash fences so Claude Code does not
// also print the commands as assistant text.
func StripWebToolMarkup(text string) string {
	s := reXMLTool.ReplaceAllString(text, "")
	s = reAMUXTool.ReplaceAllString(s, "")
	s = reBracketTool.ReplaceAllString(s, "")
	s = reToolJSON.ReplaceAllString(s, "")
	s = reBashFence.ReplaceAllString(s, "")
	return strings.TrimSpace(s)
}
