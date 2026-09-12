package types

import "testing"

func TestEmailLocalPart(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"ninhle@example.com", "ninhle"},
		{"Ninh.Le@x.com", "ninhle"},
		{"tungnt@company.io", "tungnt"},
		{"  Foo_Bar+tag@x.com ", "foobartag"},
		{"", ""},
		{"noletter", "noletter"},
		{"@@@", ""},
	}
	for _, tc := range cases {
		if got := EmailLocalPart(tc.in); got != tc.want {
			t.Errorf("EmailLocalPart(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestEmailDomainPart(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"ninhle@gmail.com", "gmailcom"},
		{"a@company.io", "companyio"},
		{"noletter", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := EmailDomainPart(tc.in); got != tc.want {
			t.Errorf("EmailDomainPart(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestAccountNamedID(t *testing.T) {
	cases := []struct {
		brand, method, email, want string
	}{
		{"claude", "web", "ninhle@x.com", "claude:web:ninhle"},
		{"claude", "web", "tungnt@company.io", "claude:web:tungnt"},
		{"chatgpt", "web", "Ninh.Le@x.com", "chatgpt:web:ninhle"},
		{"claude", "web", "", ""},
		{"", "web", "a@b.com", ""},
	}
	for _, tc := range cases {
		got := AccountNamedID(tc.brand, tc.method, tc.email)
		if got != tc.want {
			t.Errorf("AccountNamedID(%q,%q,%q) = %q, want %q",
				tc.brand, tc.method, tc.email, got, tc.want)
		}
	}
}

func TestAccountNamedIDWithDomain(t *testing.T) {
	got := AccountNamedIDWithDomain("claude", "web", "ninhle@gmail.com")
	if got != "claude:web:ninhle-gmailcom" {
		t.Fatalf("got %q", got)
	}
	got = AccountNamedIDWithDomain("claude", "web", "ninhle@company.io")
	if got != "claude:web:ninhle-companyio" {
		t.Fatalf("got %q", got)
	}
}
