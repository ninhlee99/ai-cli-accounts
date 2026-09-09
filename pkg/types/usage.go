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
