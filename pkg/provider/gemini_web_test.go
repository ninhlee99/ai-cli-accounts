package provider

import (
	"encoding/json"
	"testing"
)

func TestGeminiParseEnvelope_TextAndMeta(t *testing.T) {
	inner := []any{
		nil,
		[]any{"c_abc", "r_1", "rcid_1", nil, nil, nil, nil, nil, nil, ""},
		nil,
		nil,
		[]any{
			[]any{"rcid_1", []any{"Hello from Gemini"}, nil},
		},
	}
	innerJSON, _ := json.Marshal(inner)
	envelope := []any{"wrb.fr", nil, string(innerJSON)}
	text, meta := geminiParseEnvelope(envelope)
	if text != "Hello from Gemini" {
		t.Fatalf("text=%q", text)
	}
	if len(meta) < 3 || meta[0] != "c_abc" {
		t.Fatalf("meta=%v", meta)
	}
}

func TestGeminiDrillInt_ShortEnvelope(t *testing.T) {
	if got := geminiDrillInt([]any{"a", "b", "c"}, 5, 2, 0, 1, 0); got != 0 {
		t.Fatalf("want 0, got %d", got)
	}
	if got := geminiEnvelopeError([]any{"wrb.fr", nil, "x"}); got != 0 {
		t.Fatalf("short envelope error=%d", got)
	}
}
