package browser

import "testing"

func TestPickSessionCookieChunked(t *testing.T) {
	cookies := []cdpCookie{
		{Name: "__Secure-next-auth.session-token.0", Value: "AAA", Domain: ".chatgpt.com"},
		{Name: "__Secure-next-auth.session-token.1", Value: "BBB", Domain: ".chatgpt.com"},
		{Name: "oai-did", Value: "x", Domain: ".chatgpt.com"},
	}
	got := pickSessionCookie(cookies, ChatGPTWebLogin)
	want := "__Secure-next-auth.session-token.0=AAA; __Secure-next-auth.session-token.1=BBB"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestPickSessionCookieExact(t *testing.T) {
	cookies := []cdpCookie{
		{Name: "sessionKey", Value: "sk-test", Domain: ".claude.ai"},
	}
	got := pickSessionCookie(cookies, ClaudeWebLogin)
	if got != "sk-test" {
		t.Fatalf("got %q", got)
	}
}
