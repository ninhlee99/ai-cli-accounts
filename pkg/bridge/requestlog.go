package bridge

import (
	"net/http"
	"strings"
	"time"

	"amux-accounts/pkg/monitor"
	"amux-accounts/pkg/router"
	"amux-accounts/pkg/tools"
	"amux-accounts/pkg/types"
)

func logChatRequest(r *http.Request, pool *router.AccountPoolRouter, req *types.ChatRequest, output, stop, errStr string, inTok, outTok int, started time.Time, toolCalls []types.ToolCall) {
	dialect := "claude"
	model := ""
	input := ""
	if req != nil {
		if req.ClientDialect != "" {
			dialect = req.ClientDialect
		}
		model = req.Model
		input = monitor.LastUserText(req.Messages)
	}
	path := ""
	if r != nil {
		path = r.URL.Path
	}
	account := poolAccountLabel(pool)
	if len(toolCalls) == 0 && strings.TrimSpace(output) != "" {
		var defs []types.ToolDef
		if req != nil {
			defs = req.Tools
		}
		if len(defs) > 0 || looksWebAccount(account) {
			if parsed := tools.ParseWebTools(output, defs); len(parsed) > 0 {
				toolCalls = parsed
			}
		}
	}
	names, status := requestToolsSummary(req, toolCalls, errStr, stop)
	if errStr != "" && status == "" {
		status = "err"
	}
	if len(names) == 0 && looksWebAccount(account) && strings.TrimSpace(output) != "" {
		names = []string{"text"}
		if status == "" {
			status = "ok"
		}
	}
	now := time.Now()
	ms := now.Sub(started).Milliseconds()
	monitor.AppendRequest(types.RequestEntry{
		Time:       now,
		Dialect:    dialect,
		Path:       path,
		Account:    account,
		Model:      model,
		Input:      input,
		Output:     output,
		InTokens:   inTok,
		OutTokens:  outTok,
		DurationMs: ms,
		StopReason: stop,
		Error:      errStr,
		Tools:      names,
		ToolStatus: status,
	})
	var msgs []types.ChatMessage
	if req != nil {
		msgs = req.Messages
	}
	monitor.RecordErrorDiagnostic(monitor.ErrorDiagnostic{
		Time:       now,
		Account:    account,
		Dialect:    dialect,
		Model:      model,
		Path:       path,
		Stop:       stop,
		Error:      errStr,
		DurationMs: ms,
		Messages:   msgs,
		Output:     output,
		ToolCalls:  toolCalls,
	})
}

func pickLogOutput(streamed, logText string) string {
	if strings.TrimSpace(logText) != "" {
		return logText
	}
	return streamed
}

func looksWebAccount(account string) bool {
	a := strings.ToLower(account)
	return strings.Contains(a, "chatgpt") || strings.Contains(a, ":web")
}

// requestToolsSummary picks tool names the model asked to call, plus ok/err.
func requestToolsSummary(req *types.ChatRequest, toolCalls []types.ToolCall, errStr, stop string) (names []string, status string) {
	seen := map[string]bool{}
	add := func(n string) {
		n = strings.TrimSpace(n)
		if n == "" || seen[n] {
			return
		}
		seen[n] = true
		names = append(names, n)
	}
	for _, tc := range toolCalls {
		add(tc.Name)
	}
	if req != nil {
		for i := len(req.Messages) - 1; i >= 0; i-- {
			m := req.Messages[i]
			if strings.EqualFold(m.Role, "assistant") && len(m.ToolCalls) > 0 {
				for _, tc := range m.ToolCalls {
					add(tc.Name)
				}
				break
			}
		}
	}
	if len(names) == 0 {
		return nil, ""
	}
	if strings.TrimSpace(errStr) != "" {
		return names, "err"
	}
	// tool_use / tool_calls = gateway delivered tool requests successfully
	switch strings.ToLower(strings.TrimSpace(stop)) {
	case "tool_use", "tool_calls", "function_call":
		return names, "ok"
	default:
		return names, "ok"
	}
}
