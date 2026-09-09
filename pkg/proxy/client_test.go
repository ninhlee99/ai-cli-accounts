package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// withStubProxy points AM_PROXY_ADDR at an httptest.Server for the duration
// of the test, so ProxyUp/attachedSessions/etc. talk to a fake proxy
// instead of trying to reach a real background process on 127.0.0.1:8787.
func withStubProxy(t *testing.T, h http.Handler) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	t.Setenv("AM_PROXY_ADDR", strings.TrimPrefix(srv.URL, "http://"))
}

func TestProxyUp_True(t *testing.T) {
	withStubProxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	if !ProxyUp() {
		t.Errorf("expected ProxyUp() true when /_am/status returns 200")
	}
}

func TestProxyUp_FalseWhenNothingListening(t *testing.T) {
	// Port 1 is privileged; nothing binds it in CI or on a dev machine, so
	// the client-side Get() reliably fails to connect.
	t.Setenv("AM_PROXY_ADDR", "127.0.0.1:1")
	if ProxyUp() {
		t.Errorf("expected ProxyUp() false when nothing is listening")
	}
}

func TestProxyUp_FalseOnNon200(t *testing.T) {
	withStubProxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	if ProxyUp() {
		t.Errorf("expected ProxyUp() false on a non-200 /_am/status")
	}
}

func TestAttachedSessions(t *testing.T) {
	withStubProxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]int{"sessions": 3})
	}))
	if got := attachedSessions(); got != 3 {
		t.Errorf("expected 3 attached sessions, got %d", got)
	}
}

func TestAttachedSessions_UnreachableReturnsNegativeOne(t *testing.T) {
	// -1 is the "couldn't confirm" sentinel CmdProxyDown treats as
	// different from "confirmed 0" — losing that distinction was the
	// concern this test locks in.
	t.Setenv("AM_PROXY_ADDR", "127.0.0.1:1")
	if got := attachedSessions(); got != -1 {
		t.Errorf("expected -1 when the proxy is unreachable, got %d", got)
	}
}
