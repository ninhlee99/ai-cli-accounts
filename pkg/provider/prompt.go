package provider

import (
	"regexp"
	"strings"

	"amux-accounts/pkg/tools"
	"amux-accounts/pkg/types"
)

// PromptWithSystem prepends all system turns to userPrompt. Web adapters that
// only POST a single completion string (Claude/ChatGPT/Gemini web) must use
// this so Claude Code / client system prompts are not dropped.
func PromptWithSystem(messages []types.ChatMessage, userPrompt string) string {
	var sys strings.Builder
	for _, m := range messages {
		if strings.EqualFold(m.Role, "system") && m.Content != "" {
			if sys.Len() > 0 {
				sys.WriteString("\n\n")
			}
			sys.WriteString(m.Content)
		}
	}
	if sys.Len() == 0 {
		return userPrompt
	}
	return sys.String() + "\n\n" + userPrompt
}

const contextHandoffPreamble = `[xfer] Continue latest User. [Tool result] blocks are REAL CLI output already executed on the machine. You have active tool capabilities. Emit <tool_call> for any file or command you need. Do NOT explain you lack tools or ask user to paste. Once tool results are present, answer the user request directly.

`

// WebBackendPrompt builds the single string web UIs accept.
// FullContext (Claude Code / Codex via proxy): flatten entire history and
// skip server-side thread continuity. Interactive am chat: last user (+ system)
// when continuing a thread; flatten when starting fresh.
func WebBackendPrompt(req *types.ChatRequest, continuingThread bool) string {
	if req == nil {
		return ""
	}
	msgs := req.Messages
	if len(req.Tools) > 0 {
		// Drop Claude/Cursor harness (huge + contradicts web tools).
		// Catalog comes from this request's tools[] — new MCP/plugin/Skill
		// show up automatically, no proxy code change.
		msgs = slimWebMessages(msgs)
	}
	var body string
	if req.FullContext {
		body = BuildConcatenatedPrompt(msgs)
		if body == "" {
			return ""
		}
		body = contextHandoffPreamble + body
	} else if continuingThread {
		body = PromptWithSystem(msgs, lastUserPrompt(msgs))
	} else {
		body = BuildConcatenatedPrompt(msgs)
	}
	if len(req.Tools) > 0 {
		closer := tools.WebCloser()
		trimmedBody := strings.TrimSpace(body)
		if strings.HasSuffix(trimmedBody, "Assistant:") {
			trimmedBody = strings.TrimSuffix(trimmedBody, "Assistant:")
			body = strings.TrimSpace(trimmedBody) + "\n\n" + strings.TrimSpace(closer) + "\n\nAssistant: "
			return tools.WebPreamble(req.Tools) + body
		}
		return tools.WebPreamble(req.Tools) + body + closer
	}
	return body
}

// slimWebMessages drops client harness system turns. User/tool/assistant stay.
func slimWebMessages(msgs []types.ChatMessage) []types.ChatMessage {
	out := make([]types.ChatMessage, 0, len(msgs))
	for _, m := range msgs {
		if strings.EqualFold(m.Role, "system") && isClientHarness(m.Content) {
			continue
		}
		if strings.EqualFold(m.Role, "user") {
			c := stripWebUserNoise(m.Content)
			if c == "" {
				continue
			}
			m.Content = c
		}
		out = append(out, m)
	}
	return out
}

var (
	reSysReminder    = regexp.MustCompile(`(?s)<system-reminder>.*?</system-reminder>\s*`)
	reTotalTokens    = regexp.MustCompile(`(?s)<total_tokens>.*?</total_tokens>\s*`)
	reScratchpadHint = regexp.MustCompile(`(?im)^First privately list what you need next;[^\n]*\n?`)
	reHookNotice     = regexp.MustCompile(`(?im)^(?:SessionStart|UserPromptSubmit)\b[^\n]*\n?`)
)

func stripWebUserNoise(s string) string {
	s = reSysReminder.ReplaceAllString(s, "")
	s = reTotalTokens.ReplaceAllString(s, "")
	s = reScratchpadHint.ReplaceAllString(s, "")
	s = reHookNotice.ReplaceAllString(s, "")
	return strings.TrimSpace(s)
}

func isClientHarness(s string) bool {
	if strings.Contains(s, "You are Claude Code") || strings.Contains(s, "x-anthropic-billing-header") {
		return true
	}
	if strings.Contains(s, "You are Antigravity") || (strings.Contains(s, "Antigravity") && strings.Contains(s, "agentic")) {
		return true
	}
	low := strings.ToLower(s)
	if strings.Contains(s, "permission mode") && strings.Contains(low, "tool") {
		return true
	}
	if len([]rune(s)) > 2500 && (strings.Contains(low, "available tools") || strings.Contains(low, "input_schema")) {
		return true
	}
	return false
}
