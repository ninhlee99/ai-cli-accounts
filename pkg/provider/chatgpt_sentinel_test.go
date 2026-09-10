package provider

import (
	"strings"
	"testing"
)

func TestChatGPTAccountIDFromJWT(t *testing.T) {
	// Minimal unsigned JWT with chatgpt_account_id claim.
	// header {}, payload {"https://api.openai.com/auth":{"chatgpt_account_id":"acct-1"}}
	tok := "eyJhbGciOiJub25lIn0.eyJodHRwczovL2FwaS5vcGVuYWkuY29tL2F1dGgiOnsiY2hhdGdwdF9hY2NvdW50X2lkIjoiYWNjdC0xIn19."
	if got := chatgptAccountIDFromJWT(tok); got != "acct-1" {
		t.Fatalf("got %q want acct-1", got)
	}
	if got := chatgptAccountIDFromJWT("not-a-jwt"); got != "" {
		t.Fatalf("expected empty for garbage, got %q", got)
	}
}

func TestSolveChatGPTPoW(t *testing.T) {
	seed := "0.123456789"
	difficulty := "0fffff" // easy
	proof, err := solveChatGPTPoW(seed, difficulty, chatGPTWebUA)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(proof, "gAAAAAB") {
		t.Fatalf("proof prefix: %q", proof[:min(20, len(proof))])
	}
}
