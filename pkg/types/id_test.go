package types

import "testing"

func TestFormatID(t *testing.T) {
	cases := []struct {
		prefix string
		n      int
		want   string
	}{
		{"claudecli", 1, "claudecli:01"},
		{"claudecli", 9, "claudecli:09"},
		{"claudecli", 10, "claudecli:10"},
		{"claudecli", 123, "claudecli:123"},
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
		{"claudecli:01", "claudecli", 1, true},
		{"claudeweb:10", "claudeweb", 10, true},
		{"codexcli:123", "codexcli", 123, true},
		{"claude-web", "", 0, false},  // legacy literal ID, not the new format
		{"my-custom-provider", "", 0, false},
		{"claudecli:1", "", 0, false}, // single digit not allowed (< 2)
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

func TestFormatID_ParseID_Roundtrip(t *testing.T) {
	id := FormatID("chatgptweb", 7)
	prefix, n, ok := ParseID(id)
	if !ok || prefix != "chatgptweb" || n != 7 {
		t.Errorf("roundtrip FormatID/ParseID broke: id=%q -> (%q, %d, %v)", id, prefix, n, ok)
	}
}
