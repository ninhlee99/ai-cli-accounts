package types

import "time"

// UsageEntry records token usage for a request/response turn through the proxy.
type UsageEntry struct {
	Time    time.Time `json:"t"`
	Account string    `json:"account"`
	Model   string    `json:"model,omitempty"`
	Project string    `json:"project,omitempty"`
	Session string    `json:"session,omitempty"`
	Input   int       `json:"in"`
	Output  int       `json:"out"`
}

// EventEntry is a tagged realtime log line (rotate, failover, proxy, …).
type EventEntry struct {
	Time    time.Time `json:"t"`
	Tag     string    `json:"tag"`
	Message string    `json:"msg"`
}

// RequestEntry captures one chat turn's input/output for the watch dashboard.
type RequestEntry struct {
	Time       time.Time `json:"t"`
	Dialect    string    `json:"dialect,omitempty"` // claude|cursor|codex|…
	Path       string    `json:"path,omitempty"`
	Account    string    `json:"account,omitempty"`
	Model      string    `json:"model,omitempty"`
	Input      string    `json:"input,omitempty"`  // truncated user/prompt preview
	Output     string    `json:"output,omitempty"` // truncated assistant preview
	InTokens   int       `json:"in_tokens,omitempty"`
	OutTokens  int       `json:"out_tokens,omitempty"`
	DurationMs int64     `json:"ms,omitempty"`
	StopReason string    `json:"stop,omitempty"`
	Error      string    `json:"error,omitempty"`
	// Tools = model-requested tool names; ToolStatus "" | "ok" | "err".
	Tools      []string `json:"tools,omitempty"`
	ToolStatus string   `json:"tool_status,omitempty"`
	// Redactions = privacy scrub kinds replaced before outbound send
	// (e.g. "email", "api_key"). Never contains original secrets.
	Redactions []string `json:"redactions,omitempty"`
}
