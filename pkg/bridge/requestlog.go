package bridge

import (
	"net/http"
	"time"

	"amux-accounts/pkg/monitor"
	"amux-accounts/pkg/router"
	"amux-accounts/pkg/types"
)

func logChatRequest(r *http.Request, pool *router.AccountPoolRouter, req *types.ChatRequest, output, stop, errStr string, inTok, outTok int, started time.Time) {
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
	})
}
