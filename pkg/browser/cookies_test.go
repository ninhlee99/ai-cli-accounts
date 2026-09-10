package browser

import "testing"

func TestParseCookieHeader(t *testing.T) {
	tests := []struct {
		raw, name, want string
	}{
		{"sessionKey=abc123", "sessionKey", "abc123"},
		{"Cookie: sessionKey=abc123; other=x", "sessionKey", "abc123"},
		{"a=1; sessionKey=xyz; b=2", "sessionKey", "xyz"},
		{"eyJhbGciOi", "sessionKey", "eyJhbGciOi"}, // bare token
		{"", "sessionKey", ""},
	}
	for _, tt := range tests {
		got := ParseCookieHeader(tt.raw, tt.name)
		if got != tt.want {
			t.Errorf("ParseCookieHeader(%q,%q)=%q want %q", tt.raw, tt.name, got, tt.want)
		}
	}
}
