package proxy

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestIssueNewAuthToken_AmuxPrefixAndEphemeral(t *testing.T) {
	withTempAmHome(t)

	tok1, err := IssueNewAuthToken()
	if err != nil {
		t.Fatalf("IssueNewAuthToken 1: %v", err)
	}
	if !strings.HasPrefix(tok1, AuthTokenPrefix) {
		t.Fatalf("expected token prefix %q, got %q", AuthTokenPrefix, tok1)
	}

	tok2, err := IssueNewAuthToken()
	if err != nil {
		t.Fatalf("IssueNewAuthToken 2: %v", err)
	}
	if !strings.HasPrefix(tok2, AuthTokenPrefix) {
		t.Fatalf("expected token prefix %q, got %q", AuthTokenPrefix, tok2)
	}
	if tok1 == tok2 {
		t.Fatalf("expected new ephemeral key on each issuance, got identical: %q", tok1)
	}

	loaded, err := LoadAuthToken()
	if err != nil {
		t.Fatalf("LoadAuthToken: %v", err)
	}
	if loaded != tok2 {
		t.Fatalf("loaded token %q != latest issued %q", loaded, tok2)
	}

	if err := ClearAuthToken(); err != nil {
		t.Fatalf("ClearAuthToken: %v", err)
	}
	if _, err := LoadAuthToken(); err == nil {
		t.Fatal("expected error after ClearAuthToken, got nil")
	}
}

func TestRequireAuth_RejectsSampleOrDummyKeysOnPublicHost(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	sessionKey := "amux-112233445566778899aabbcc"
	h := requireAuth(sessionKey, inner)

	// Sample / dummy keys on non-loopback MUST be rejected (401).
	forbiddenKeys := []struct {
		header string
		val    string
	}{
		{"Authorization", "Bearer dummy"},
		{"Authorization", "Bearer sample"},
		{"Authorization", "Bearer sk-ant-api03-test"},
		{"X-Api-Key", "dummy"},
		{"X-Api-Key", "sample"},
		{"X-Api-Key", "anything-random"},
		{"api-key", "dummy"},
		{"X-Am-Token", "dummy"},
	}

	for _, fk := range forbiddenKeys {
		r := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		r.RemoteAddr = "192.168.1.50:54321" // public / external host
		r.Header.Set(fk.header, fk.val)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("public host with sample key %s=%q: got %d, want 401", fk.header, fk.val, w.Code)
		}
	}

	// Loopback requests CAN use dummy/sample keys or be empty.
	rLocal := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	rLocal.RemoteAddr = "127.0.0.1:54321"
	rLocal.Header.Set("Authorization", "Bearer dummy")
	wLocal := httptest.NewRecorder()
	h.ServeHTTP(wLocal, rLocal)
	if wLocal.Code != http.StatusOK {
		t.Errorf("loopback with dummy key: got %d, want 200", wLocal.Code)
	}

	// Public host with the issued session key MUST succeed via any supported header.
	validHeaders := []struct {
		header string
		val    string
	}{
		{"X-Api-Key", sessionKey},
		{"Authorization", "Bearer " + sessionKey},
		{"api-key", sessionKey},
		{"X-Am-Token", sessionKey},
	}

	for _, vh := range validHeaders {
		r := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		r.RemoteAddr = "192.168.1.50:54321"
		r.Header.Set(vh.header, vh.val)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Errorf("public host with valid session key %s: got %d, want 200", vh.header, w.Code)
		}
	}
}

func TestRequireAuth_RateLimiting(t *testing.T) {
	withTempAmHome(t)
	token := "amux-test-rate-limit-token-12345"
	h := requireAuth(token, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	attackerIP := "203.0.113.88:12345"

	// 10 failed attempts should be 401
	for i := 0; i < 10; i++ {
		r := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		r.RemoteAddr = attackerIP
		r.Header.Set("Authorization", "Bearer wrong-key")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: got %d, want 401", i+1, w.Code)
		}
	}

	// 11th attempt should be 429 Too Many Requests
	rBlocked := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	rBlocked.RemoteAddr = attackerIP
	rBlocked.Header.Set("Authorization", "Bearer wrong-key")
	wBlocked := httptest.NewRecorder()
	h.ServeHTTP(wBlocked, rBlocked)
	if wBlocked.Code != http.StatusTooManyRequests {
		t.Fatalf("blocked attempt: got %d, want 429", wBlocked.Code)
	}

	// But a different IP is NOT blocked
	cleanIP := "203.0.113.99:54321"
	rClean := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	rClean.RemoteAddr = cleanIP
	rClean.Header.Set("Authorization", "Bearer "+token)
	wClean := httptest.NewRecorder()
	h.ServeHTTP(wClean, rClean)
	if wClean.Code != http.StatusOK {
		t.Fatalf("clean IP: got %d, want 200", wClean.Code)
	}
}

func TestRecordFailedAuth_MaxIPCapPreventsUnboundedGrowth(t *testing.T) {
	// Reset state for this test by temporarily swapping the map.
	authLimiterMu.Lock()
	orig := failedAttempts
	failedAttempts = make(map[string][]time.Time)
	authLimiterMu.Unlock()
	defer func() {
		authLimiterMu.Lock()
		failedAttempts = orig
		authLimiterMu.Unlock()
	}()

	// Record more IPs than the cap allows.
	overflow := maxFailedAuthIPs + 1000
	for i := 0; i < overflow; i++ {
		// Use unique fake IPs
		ip := fmt.Sprintf("10.%d.%d.%d", (i>>16)&0xFF, (i>>8)&0xFF, i&0xFF)
		recordFailedAuth(ip)
	}

	authLimiterMu.Lock()
	size := len(failedAttempts)
	authLimiterMu.Unlock()

	if size > maxFailedAuthIPs {
		t.Errorf("failedAttempts grew to %d, max allowed is %d (OOM risk)", size, maxFailedAuthIPs)
	}
}


func TestStopAuthRateLimiter_IdempotentNoPanic(t *testing.T) {
	// StopAuthRateLimiter uses sync.Once internally — calling it more than once
	// must never panic (no double-close). We can't reset Once state in tests,
	// so we verify the exported function is safe to call when already stopped.
	// The package-level goroutine may already be stopped by other tests; that's fine.
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("StopAuthRateLimiter panicked: %v", r)
		}
	}()
	// Call multiple times — all calls after the first are no-ops.
	StopAuthRateLimiter()
	StopAuthRateLimiter()
	StopAuthRateLimiter()
}

func TestRecordFailedAuth_PerIPSliceBounded(t *testing.T) {
	ip := "192.168.1.100"
	authLimiterMu.Lock()
	delete(failedAttempts, ip)
	authLimiterMu.Unlock()
	defer func() {
		authLimiterMu.Lock()
		delete(failedAttempts, ip)
		authLimiterMu.Unlock()
	}()

	// Simulate 100 failed auth attempts from the same IP
	for i := 0; i < 100; i++ {
		recordFailedAuth(ip)
	}

	authLimiterMu.Lock()
	count := len(failedAttempts[ip])
	authLimiterMu.Unlock()

	if count > maxFailedAuthPerIP {
		t.Errorf("failedAttempts[%q] slice grew to %d, want <= %d", ip, count, maxFailedAuthPerIP)
	}
	if !isAuthRateLimited(ip) {
		t.Errorf("expected IP %s to be rate limited", ip)
	}
}
