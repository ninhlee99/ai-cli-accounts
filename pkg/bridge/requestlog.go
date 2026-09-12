package bridge

import (
	"net/http"
	"strings"
	"time"

	"amux-accounts/pkg/monitor"
	"amux-accounts/pkg/router"
	"amux-accounts/pkg/types"
)

func logChatRequest(r *http.Request, pool *router.AccountPoolRouter, req *types.ChatRequest, output, stop, errStr string, inTok, outTok int, started time.Time, toolCalls []types.ToolCall) {
	if req == nil {
		return
	}
	dialect := req.ClientDialect
	if dialect == "" {
		dialect = "unknown"
	}
	path := ""
	if r != nil {
		path = r.URL.Path
	}
	tools, status := requestToolsSummary(req, toolCalls, errStr, stop)
	monitor.AppendRequest(types.RequestEntry{
		Time:       time.Now(),
		Dialect:    dialect,
		Path:       path,
		Account:    poolAccountLabel(pool),
		Model:      req.Model,
		Input:      monitor.LastUserText(req.Messages),
		Output:     output,
		InTokens:   inTok,
		OutTokens:  outTok,
		DurationMs: time.Since(started).Milliseconds(),
		StopReason: stop,
		Error:      errStr,
		Tools:      tools,
		ToolStatus: status,
	})
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
