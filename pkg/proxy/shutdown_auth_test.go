package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"amux-accounts/pkg/router"
	"amux-accounts/pkg/types"
)

// TestShutdown_GraceDrainStillRequiresAuth guards against a regression where
// /_am/shutdown's grace-drain handler (see newPassthroughHandler in
// server.go) was swapped into the swappableHandler without the requireAuth
// wrapper the rest of the server uses on a public bind — letting any
// non-loopback client reach the live Anthropic-direct passthrough,
// unauthenticated, for the shutdownGrace window.
func TestShutdown_GraceDrainStillRequiresAuth(t *testing.T) {
	t.Setenv("AM_HOME", t.TempDir())
	t.Setenv("ANTHROPIC_API_KEY", "")

	rot := NewRotator("claude")
	life := NewLifecycle()
	life.AddSession(12345) // ensure Sessions() > 0 so shutdown takes the drain path
	mode := &ProxyMode{}
	pool := router.NewAccountPoolRouter([]types.ProviderAdapter{&stubAdapter{id: "stub"}})
	rp := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unexpected reverse-proxy call in test", http.StatusTeapot)
	})
	sw := &swappableHandler{}

	const token = "regression-test-token"
	h := newHandler(rot, life, mode, pool, pool, rp, "https://api.anthropic.com", sw, token, func() {})
	h = requireAuth(token, h)
	sw.Set(h)

	// Trigger shutdown (simulating a public-bind daemon told to go down).
	shutReq := httptest.NewRequest(http.MethodPost, "/_am/shutdown", nil)
	shutReq.RemoteAddr = "127.0.0.1:9999" // admin call itself is loopback/local
	shutW := httptest.NewRecorder()
	sw.ServeHTTP(shutW, shutReq)
	if shutW.Code != http.StatusOK {
		t.Fatalf("shutdown call: got %d, want 200", shutW.Code)
	}

	// Give the goroutine time to swap in the grace-drain passthrough handler.
	time.Sleep(50 * time.Millisecond)

	// A remote, unauthenticated client must still be rejected during the
	// grace-drain window, exactly as it would be before shutdown.
	msgReq := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	msgReq.RemoteAddr = "203.0.113.9:12345"
	msgW := httptest.NewRecorder()
	sw.ServeHTTP(msgW, msgReq)

	if msgW.Code != http.StatusUnauthorized {
		t.Fatalf("grace-drain passthrough: unauthenticated remote request got %d, want 401 (auth bypass regression)", msgW.Code)
	}
}
