package browser

import "testing"

func TestParseClaudeAccountEmail(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"flat", `{"email":"ninhle@x.com","id":"1"}`, "ninhle@x.com"},
		{"account", `{"account":{"email":"tungnt@y.com"}}`, "tungnt@y.com"},
		{"user", `{"user":{"email":"a@b.com"}}`, "a@b.com"},
		{"empty", `{}`, ""},
		{"bad", `not-json`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseClaudeAccountEmail([]byte(tc.body)); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
