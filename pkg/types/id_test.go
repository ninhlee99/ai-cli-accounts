package types

import "testing"

func TestFormatID(t *testing.T) {
	cases := []struct {
		prefix string
		n      int
		want   string
	}{
		{"claude:code", 1, "claude:code:01"},
		{"gemini:api", 9, "gemini:api:09"},
		{"chatgpt", 10, "chatgpt:10"},
		{"codex", 123, "codex:123"},
	}
	for _, tc := range cases {
		if got := FormatID(tc.prefix, tc.n); got != tc.want {
			t.Errorf("FormatID(%q, %d) = %q, want %q", tc.prefix, tc.n, got, tc.want)
		}
	}
}

func TestParseID(t *testing.T) {
	cases := []struct {
		id         string
		wantPrefix string
		wantN      int
		wantOK     bool
	}{
		{"claude:code:01", "claude:code", 1, true},
		{"claude:web:10", "claude:web", 10, true},
		{"gemini:api:01", "gemini:api", 1, true},
		{"chatgpt:01", "chatgpt", 1, true},
		{"codex:123", "codex", 123, true},
		{"geminiapi:01", "geminiapi", 1, true}, // old flat still parses
		{"claude:web:ninhle", "", 0, false},    // named identity, not numeric
		{"claude-web", "", 0, false},
		{"my-custom-provider", "", 0, false},
		{"chatgpt:1", "", 0, false},
		{"", "", 0, false},
	}
	for _, tc := range cases {
		prefix, n, ok := ParseID(tc.id)
		if ok != tc.wantOK || prefix != tc.wantPrefix || n != tc.wantN {
			t.Errorf("ParseID(%q) = (%q, %d, %v), want (%q, %d, %v)",
				tc.id, prefix, n, ok, tc.wantPrefix, tc.wantN, tc.wantOK)
		}
	}
}

func TestMigrateCompactID(t *testing.T) {
	cases := []struct {
		in, want string
		changed  bool
	}{
		{"geminiapi:01", "gemini:api:01", true},
		{"claudeweb:02", "claude:web:02", true},
		{"chatgptweb:01", "chatgpt:01", true},
		{"geminiweb:01", "gemini:web:01", true},
		{"codexcli:01", "codex:01", true},
		{"openrouter", "openrouter:api:01", true},
		{"openrouter:api", "openrouter:api:01", true},
		{"gemini:api:01", "gemini:api:01", false},
		{"claude:web:ninhle", "claude:web:ninhle", false},
		{"my-custom", "my-custom", false},
	}
	for _, tc := range cases {
		got, ch := MigrateCompactID(tc.in)
		if got != tc.want || ch != tc.changed {
			t.Errorf("MigrateCompactID(%q) = (%q, %v), want (%q, %v)",
				tc.in, got, ch, tc.want, tc.changed)
		}
	}
}

func TestRemapAccountIDsInText(t *testing.T) {
	cases := map[string]string{
		"active provider → geminiapi:01": "active provider → gemini:api:01",
		"pin openrouter":                 "pin openrouter:api:01",
		"use openrouter:api next":        "use openrouter:api:01 next",
		"already gemini:api:01":          "already gemini:api:01",
		"keep openrouter:api:02":         "keep openrouter:api:02",
	}
	for in, want := range cases {
		if got := RemapAccountIDsInText(in); got != want {
			t.Errorf("RemapAccountIDsInText(%q)=%q want %q", in, got, want)
		}
	}
}

func TestFormatID_ParseID_Roundtrip(t *testing.T) {
	id := FormatID("gemini:api", 7)
	prefix, n, ok := ParseID(id)
	if !ok || prefix != "gemini:api" || n != 7 {
		t.Errorf("roundtrip broke: id=%q -> (%q, %d, %v)", id, prefix, n, ok)
	}
}
