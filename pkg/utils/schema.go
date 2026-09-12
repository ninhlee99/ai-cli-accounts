// Package utils holds small shared helpers used across packages
// (JSON Schema normalization, etc.).
package utils

import "encoding/json"

// NormalizeJSONSchema returns a usable JSON Schema object. Empty or null
// input becomes {"type":"object","properties":{}}.
func NormalizeJSONSchema(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	return raw
}
