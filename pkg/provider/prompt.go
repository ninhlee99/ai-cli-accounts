package provider

import (
	"strings"

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

// contextHandoffPreamble is prepended when a coding-agent client (Claude Code,
// Codex, …) sends FullContext so the web model treats the flattened transcript
// as the task to continue — not a fresh chat.
const contextHandoffPreamble = `[Context transfer from coding agent]
You are continuing an in-progress task. The transcript below is the full client history (including prior tool calls/results as text).
Do the latest User request with that context. Do not claim you lack prior context if it appears below.
Tools (shell/files/edit) are owned by the coding agent CLI — answer with concrete next steps, code, or commands the agent can run; do not invent tool results.

`

// WebBackendPrompt builds the single string web UIs accept.
// FullContext (Claude Code / Codex via proxy): flatten entire history and
// skip server-side thread continuity. Interactive am chat: last user (+ system)
// when continuing a thread; flatten when starting fresh.
func WebBackendPrompt(req *types.ChatRequest, continuingThread bool) string {
	if req == nil {
		return ""
	}
	if req.FullContext {
		body := BuildConcatenatedPrompt(req.Messages)
		if body == "" {
			return ""
		}
		return contextHandoffPreamble + body
	}
	if continuingThread {
		return PromptWithSystem(req.Messages, lastUserPrompt(req.Messages))
	}
	return BuildConcatenatedPrompt(req.Messages)
}
