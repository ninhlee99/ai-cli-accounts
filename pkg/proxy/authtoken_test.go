package proxy

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func withTempAmHome(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("AM_HOME", dir)
}

func TestLoadOrCreateAuthToken_PersistsAndReuses(t *testing.T) {
	withTempAmHome(t)

	tok1, err := LoadOrCreateAuthToken()
	if err != nil {
		t.Fatalf("first LoadOrCreateAuthToken: %v", err)
	}
	if tok1 == "" {
		t.Fatal("expected non-empty token")
	}

	tok2, err := LoadOrCreateAuthToken()
	if err != nil {
		t.Fatalf("second LoadOrCreateAuthToken: %v", err)
	}
	if tok1 != tok2 {
		t.Fatalf("token not stable across calls: %q vs %q", tok1, tok2)
	}

	p := authTokenPath()
	info, err := os.Stat(p)
	if err != nil {
		t.Fatalf("stat token file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("token file perm = %o, want 0600", perm)
	}
}

func TestIsLoopback(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:54321", true},
		{"[::1]:54321", true},
		{"10.0.0.5:54321", false},
		{"203.0.113.9:443", false},
		{"not-an-addr", false},
	}
	for _, c := range cases {
		r := &http.Request{RemoteAddr: c.addr}
		if got := isLoopback(r); got != c.want {
			t.Errorf("isLoopback(%q) = %v, want %v", c.addr, got, c.want)
		}
	}
}

func TestRequestToken(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if got := requestToken(r); got != "" {
		t.Fatalf("expected empty token, got %q", got)
	}

	r = httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Am-Token", "abc123")
	if got := requestToken(r); got != "abc123" {
		t.Fatalf("X-Am-Token: got %q", got)
	}

	r = httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer xyz789")
	if got := requestToken(r); got != "xyz789" {
		t.Fatalf("Authorization bearer: got %q", got)
	}

	// X-Am-Token takes precedence when both are set.
	r = httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Am-Token", "first")
	r.Header.Set("Authorization", "Bearer second")
	if got := requestToken(r); got != "first" {
		t.Fatalf("precedence: got %q, want %q", got, "first")
	}
}

func TestRequireAuth_LoopbackNeverChallenged(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := requireAuth("secret-token", inner)

	r := httptest.NewRequest(http.MethodGet, "/v1/messages", nil)
	r.RemoteAddr = "127.0.0.1:9999"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("loopback request without token: got %d, want 200", w.Code)
	}
}

func TestRequireAuth_EmptyTokenDisablesGate(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := requireAuth("", inner)

	r := httptest.NewRequest(http.MethodGet, "/v1/messages", nil)
	r.RemoteAddr = "203.0.113.9:12345"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("empty-token gate: got %d, want 200", w.Code)
	}
}

func TestRequireAuth_NonLoopbackRejectsMissingOrWrongToken(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := requireAuth("correct-token", inner)

	// No token presented.
	r := httptest.NewRequest(http.MethodGet, "/v1/messages", nil)
	r.RemoteAddr = "203.0.113.9:12345"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("missing token: got %d, want 401", w.Code)
	}

	// Wrong token presented.
	r = httptest.NewRequest(http.MethodGet, "/v1/messages", nil)
	r.RemoteAddr = "203.0.113.9:12345"
	r.Header.Set("X-Am-Token", "wrong-token")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: got %d, want 401", w.Code)
	}
}

func TestRequireAuth_NonLoopbackAcceptsCorrectToken(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := requireAuth("correct-token", inner)

	r := httptest.NewRequest(http.MethodGet, "/v1/messages", nil)
	r.RemoteAddr = "203.0.113.9:12345"
	r.Header.Set("X-Am-Token", "correct-token")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("correct token: got %d, want 200", w.Code)
	}

	// Also via Authorization: Bearer.
	r = httptest.NewRequest(http.MethodGet, "/v1/messages", nil)
	r.RemoteAddr = "203.0.113.9:12345"
	r.Header.Set("Authorization", "Bearer correct-token")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("correct bearer token: got %d, want 200", w.Code)
	}
}

func TestAuthTokenPath_UnderBaseDir(t *testing.T) {
	withTempAmHome(t)
	want := filepath.Join(os.Getenv("AM_HOME"), "proxy.token")
	if got := authTokenPath(); got != want {
		t.Fatalf("authTokenPath() = %q, want %q", got, want)
	}
}
