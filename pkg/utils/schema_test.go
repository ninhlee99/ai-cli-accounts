package utils

import (
	"encoding/json"
	"testing"
)

func TestNormalizeJSONSchema(t *testing.T) {
	got := NormalizeJSONSchema(nil)
	if string(got) != `{"type":"object","properties":{}}` {
		t.Fatalf("nil → %s", got)
	}
	in := json.RawMessage(`{"type":"object"}`)
	if string(NormalizeJSONSchema(in)) != `{"type":"object"}` {
		t.Fatalf("passthrough failed")
	}
}
