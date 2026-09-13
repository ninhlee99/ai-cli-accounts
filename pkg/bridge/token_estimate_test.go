package bridge_test

import (
	"encoding/json"
	"testing"

	"amux-accounts/pkg/bridge"
	"amux-accounts/pkg/types"
)

// ──────────────────────────────────────────────────────────────────────────────
// EstimateStringTokens
// ──────────────────────────────────────────────────────────────────────────────

func TestEstimateStringTokens_Empty(t *testing.T) {
	if got := bridge.EstimateStringTokens(""); got != 0 {
		t.Errorf("empty string: want 0, got %d", got)
	}
}

func TestEstimateStringTokens_PureASCII(t *testing.T) {
	// "hello" = 5 chars → (5+3)/4 = 2 tokens
	got := bridge.EstimateStringTokens("hello")
	if got != 2 {
		t.Errorf("\"hello\": want 2, got %d", got)
	}

	// "hello world" = 11 chars → (11+3)/4 = 3 tokens
	got = bridge.EstimateStringTokens("hello world")
	if got != 3 {
		t.Errorf("\"hello world\": want 3, got %d", got)
	}

	// Exactly 4 ASCII chars → (4+3)/4 = 1 token
	got = bridge.EstimateStringTokens("abcd")
	if got != 1 {
		t.Errorf("\"abcd\": want 1, got %d", got)
	}

	// Exactly 8 ASCII chars → (8+3)/4 = 2 tokens
	got = bridge.EstimateStringTokens("abcdefgh")
	if got != 2 {
		t.Errorf("\"abcdefgh\": want 2, got %d", got)
	}
}

func TestEstimateStringTokens_MultibyteUTF8(t *testing.T) {
	// "xin chào" — "xin ch" is ASCII (6 chars), "à" and "o" — "chào" breakdown:
	// 'x','i','n',' ','c','h' = 6 ASCII, 'à' = multibyte (1 token), 'o' = ASCII (1)
	// ASCII count: "xin ch" = 6 + "o" = 7 → (7+3)/4 = 2 tokens, multibyte = 1 → total = 3
	got := bridge.EstimateStringTokens("xin chào")
	if got < 2 || got > 5 {
		t.Errorf("\"xin chào\": want reasonable 2-5 tokens, got %d", got)
	}

	// Pure Vietnamese: "xin chào thế giới" should produce more tokens than ascii equivalent
	viet := bridge.EstimateStringTokens("xin chào thế giới")
	ascii := bridge.EstimateStringTokens("xin chao the gioi")
	if viet <= ascii {
		t.Errorf("Vietnamese should produce >= ASCII equivalent: viet=%d, ascii=%d", viet, ascii)
	}

	// CJK: each character is 1 token
	// "你好" = 2 CJK chars → 0 ASCII + 2 multibyte = 2 tokens
	got = bridge.EstimateStringTokens("你好")
	if got != 2 {
		t.Errorf("\"你好\": want 2 tokens (1 per CJK char), got %d", got)
	}

	// "中文测试" = 4 CJK chars → 4 tokens
	got = bridge.EstimateStringTokens("中文测试")
	if got != 4 {
		t.Errorf("\"中文测试\": want 4 tokens, got %d", got)
	}
}

func TestEstimateStringTokens_Emoji(t *testing.T) {
	// Single emoji "🚀" → 0 ASCII + 1 multibyte = 1 token
	got := bridge.EstimateStringTokens("🚀")
	if got != 1 {
		t.Errorf("\"🚀\": want 1 token, got %d", got)
	}

	// "Hello 🌍" = "Hello " (6 ASCII) + emoji (1 multibyte)
	// ASCII: (6+3)/4 = 2, multibyte: 1 → total = 3
	got = bridge.EstimateStringTokens("Hello 🌍")
	if got != 3 {
		t.Errorf("\"Hello 🌍\": want 3 tokens, got %d", got)
	}

	// Multiple emojis "🚀🌍🎉" → 3 tokens
	got = bridge.EstimateStringTokens("🚀🌍🎉")
	if got != 3 {
		t.Errorf("\"🚀🌍🎉\": want 3 tokens, got %d", got)
	}
}

func TestEstimateStringTokens_Mixed(t *testing.T) {
	// "API: 你好 🚀" = "API: " (5 ASCII) + "你好" (2 multibyte) + " " (1 ASCII) + "🚀" (1 multibyte)
	// ASCII: 6, multibyte: 3
	// ASCII tokens: (6+3)/4 = 2, multibyte tokens: 3 → total = 5
	got := bridge.EstimateStringTokens("API: 你好 🚀")
	if got != 5 {
		t.Errorf("\"API: 你好 🚀\": want 5 tokens, got %d", got)
	}
}

func TestEstimateStringTokens_LongASCII(t *testing.T) {
	// 100-char ASCII string → (100+3)/4 = 25 tokens
	s := ""
	for i := 0; i < 100; i++ {
		s += "a"
	}
	got := bridge.EstimateStringTokens(s)
	if got != 25 {
		t.Errorf("100 ASCII chars: want 25, got %d", got)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// EstimateInputTokens
// ──────────────────────────────────────────────────────────────────────────────

func TestEstimateInputTokens_Nil(t *testing.T) {
	if got := bridge.EstimateInputTokens(nil); got != 0 {
		t.Errorf("nil req: want 0, got %d", got)
	}
}

func TestEstimateInputTokens_EmptyRequest(t *testing.T) {
	req := &types.ChatRequest{}
	// 3 (envelope) + no messages + no tools → but clamped to min 1
	got := bridge.EstimateInputTokens(req)
	if got < 1 {
		t.Errorf("empty request: want >= 1, got %d", got)
	}
}

func TestEstimateInputTokens_SingleMessage(t *testing.T) {
	req := &types.ChatRequest{
		Messages: []types.ChatMessage{
			{Role: "user", Content: "Hello"},
		},
	}
	// 3 (envelope) + 4 (turn) + EstimateStringTokens("Hello")=2 = 9
	got := bridge.EstimateInputTokens(req)
	want := 3 + 4 + bridge.EstimateStringTokens("Hello")
	if got != want {
		t.Errorf("single message: want %d, got %d", want, got)
	}
}

func TestEstimateInputTokens_MultipleMessages(t *testing.T) {
	req := &types.ChatRequest{
		Messages: []types.ChatMessage{
			{Role: "system", Content: "You are helpful."},
			{Role: "user", Content: "Hello"},
			{Role: "assistant", Content: "Hi there!"},
		},
	}
	want := 3 // envelope
	for _, m := range req.Messages {
		want += 4 + bridge.EstimateStringTokens(m.Content)
	}
	got := bridge.EstimateInputTokens(req)
	if got != want {
		t.Errorf("multiple messages: want %d, got %d", want, got)
	}
}

func TestEstimateInputTokens_ToolDefinitions(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}}}`)
	req := &types.ChatRequest{
		Messages: []types.ChatMessage{
			{Role: "user", Content: "run it"},
		},
		Tools: []types.ToolDef{
			{Name: "Bash", Description: "Execute shell commands", InputSchema: schema},
		},
	}
	want := 3 + 4 + bridge.EstimateStringTokens("run it") // messages part
	want += 8 + bridge.EstimateStringTokens("Bash") +
		bridge.EstimateStringTokens("Execute shell commands") +
		bridge.EstimateStringTokens(string(schema)) // tool part
	got := bridge.EstimateInputTokens(req)
	if got != want {
		t.Errorf("with tool def: want %d, got %d", want, got)
	}
}

func TestEstimateInputTokens_MultipleToolDefs(t *testing.T) {
	req := &types.ChatRequest{
		Messages: []types.ChatMessage{
			{Role: "user", Content: "help"},
		},
		Tools: []types.ToolDef{
			{Name: "Bash", Description: "shell", InputSchema: json.RawMessage(`{}`)},
			{Name: "Read", Description: "read file", InputSchema: json.RawMessage(`{}`)},
			{Name: "Write", Description: "write file", InputSchema: json.RawMessage(`{}`)},
		},
	}
	// Each tool adds 8 overhead + name + desc + schema
	got := bridge.EstimateInputTokens(req)
	if got <= 0 {
		t.Errorf("multiple tool defs: want positive, got %d", got)
	}
	// Sanity: 3 tools add at least 3*8=24 overhead tokens
	base := bridge.EstimateInputTokens(&types.ChatRequest{Messages: req.Messages})
	if got < base+24 {
		t.Errorf("3 tools should add >=24 tokens overhead: base=%d, got=%d", base, got)
	}
}

func TestEstimateInputTokens_ToolCallsInMessage(t *testing.T) {
	req := &types.ChatRequest{
		Messages: []types.ChatMessage{
			{
				Role:    "assistant",
				Content: "",
				ToolCalls: []types.ToolCall{
					{ID: "tc1", Name: "Bash", Arguments: `{"command":"ls -la"}`},
				},
			},
		},
	}
	// 3 + 4 (turn) + 0 (content) + EstimateStringTokens("Bash") + EstimateStringTokens(`{"command":"ls -la"}`) + 4
	got := bridge.EstimateInputTokens(req)
	want := 3 + 4 + 0 +
		bridge.EstimateStringTokens("Bash") +
		bridge.EstimateStringTokens(`{"command":"ls -la"}`) + 4
	if got != want {
		t.Errorf("tool call in message: want %d, got %d", want, got)
	}
}

func TestEstimateInputTokens_EnvelopeOverheadMonotonicallyIncreases(t *testing.T) {
	base := bridge.EstimateInputTokens(&types.ChatRequest{})
	oneMsg := bridge.EstimateInputTokens(&types.ChatRequest{
		Messages: []types.ChatMessage{{Role: "user", Content: "hi"}},
	})
	twoMsg := bridge.EstimateInputTokens(&types.ChatRequest{
		Messages: []types.ChatMessage{
			{Role: "user", Content: "hi"},
			{Role: "assistant", Content: "hello"},
		},
	})
	if oneMsg <= base {
		t.Errorf("adding a message should increase tokens: base=%d, oneMsg=%d", base, oneMsg)
	}
	if twoMsg <= oneMsg {
		t.Errorf("adding another message should increase tokens: oneMsg=%d, twoMsg=%d", oneMsg, twoMsg)
	}
}

func TestEstimateInputTokens_PerTurnOverheadIs4(t *testing.T) {
	// Difference between 0 and 1 message should be exactly 4 + content tokens
	req0 := &types.ChatRequest{}
	req1 := &types.ChatRequest{
		Messages: []types.ChatMessage{{Role: "user", Content: ""}},
	}
	delta := bridge.EstimateInputTokens(req1) - bridge.EstimateInputTokens(req0)
	// Empty content = 0 tokens, so delta should be exactly 4 (per-turn overhead)
	if delta != 4 {
		t.Errorf("per-turn overhead should be 4, got %d", delta)
	}
}

func TestEstimateInputTokens_PerToolDefOverheadIs8(t *testing.T) {
	// Same messages, 1 tool def with empty content → overhead should be exactly 8
	schema := json.RawMessage(`""`) // near-zero schema content
	req0 := &types.ChatRequest{Messages: []types.ChatMessage{{Role: "user", Content: "x"}}}
	req1 := &types.ChatRequest{
		Messages: []types.ChatMessage{{Role: "user", Content: "x"}},
		Tools:    []types.ToolDef{{Name: "", Description: "", InputSchema: schema}},
	}
	schemaTokens := bridge.EstimateStringTokens(string(schema))
	delta := bridge.EstimateInputTokens(req1) - bridge.EstimateInputTokens(req0)
	want := 8 + schemaTokens
	if delta != want {
		t.Errorf("per-tool-def overhead: want %d, got %d", want, delta)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// EstimateBytesTokens — must be consistent with EstimateStringTokens(string(b))
// ──────────────────────────────────────────────────────────────────────────────

func TestEstimateBytesTokens_ConsistentWithStringVersion(t *testing.T) {
	cases := []string{
		"",
		"hello",
		"hello world",
		"你好世界",
		"🚀🌍🎉",
		"API: 你好 🚀",
		`{"type":"object","properties":{"command":{"type":"string"}}}`,
		"xin chào thế giới",
	}
	for _, s := range cases {
		fromString := bridge.EstimateStringTokens(s)
		fromBytes := bridge.EstimateBytesTokens([]byte(s))
		if fromBytes != fromString {
			t.Errorf("inconsistency for %q: EstimateStringTokens=%d, EstimateBytesTokens=%d",
				s, fromString, fromBytes)
		}
	}
}

func TestEstimateBytesTokens_NilAndEmpty(t *testing.T) {
	if got := bridge.EstimateBytesTokens(nil); got != 0 {
		t.Errorf("nil bytes: want 0, got %d", got)
	}
	if got := bridge.EstimateBytesTokens([]byte{}); got != 0 {
		t.Errorf("empty bytes: want 0, got %d", got)
	}
}

func TestEstimateBytesTokens_JSONSchema(t *testing.T) {
	schema := []byte(`{"type":"object","required":["command"],"properties":{"command":{"type":"string","description":"Shell command to execute"}}}`)
	got := bridge.EstimateBytesTokens(schema)
	// Should be same as string version
	want := bridge.EstimateStringTokens(string(schema))
	if got != want {
		t.Errorf("JSON schema: EstimateStringTokens=%d, EstimateBytesTokens=%d", want, got)
	}
	// Sanity: non-trivial schema should produce > 0 tokens
	if got == 0 {
		t.Errorf("non-empty schema should produce > 0 tokens, got 0")
	}
}

func TestEstimateBytesTokens_InvalidUTF8(t *testing.T) {
	// Sequences of invalid / corrupted UTF-8 bytes:
	// 0xFF, 0xFE are invalid UTF-8 start bytes
	// 0x80 is a solitary continuation byte
	// 0xC2 without trailing byte
	cases := [][]byte{
		{0xFF, 0xFF, 0xFF},
		{0x80, 0x81, 0x82},
		{'a', 'b', 0xFF, 'c', 'd'},
		{0xC2}, // incomplete 2-byte sequence
		{0xE0, 0xA0}, // incomplete 3-byte sequence
		{0xF0, 0x90, 0x80}, // incomplete 4-byte sequence
	}
	for _, b := range cases {
		fromBytes := bridge.EstimateBytesTokens(b)
		fromString := bridge.EstimateStringTokens(string(b))
		if fromBytes != fromString {
			t.Errorf("invalid UTF-8 %v: EstimateBytesTokens=%d, EstimateStringTokens=%d",
				b, fromBytes, fromString)
		}
	}
}
