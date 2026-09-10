package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"amux-accounts/pkg/router"
	"amux-accounts/pkg/types"
)

// stubAdapter is a minimal types.ProviderAdapter for exercising the pool
// endpoints and the /v1/chat/completions and /v1/messages routes without a
// real upstream.
type stubAdapter struct {
	id string
}

func (s *stubAdapter) ID() string    { return s.id }
func (s *stubAdapter) Priority() int { return 1 }

func (s *stubAdapter) SendMessageStream(ctx context.Context, req *types.ChatRequest) (<-chan types.StreamChunk, error) {
	ch := make(chan types.StreamChunk, 1)
	ch <- types.StreamChunk{ID: s.id, Content: "hi", Done: true}
	close(ch)
	return ch, nil
}

// newTestHandler builds a newHandler() with an isolated AM_HOME (so it
// never touches the real ~/.am or system keychain), a single stub pool
// adapter, and a reverse-proxy stand-in that fails loudly if it's ever
// actually invoked — tests that expect the pool/bridge path to be taken
// assert they never hit it.
func newTestHandler(t *testing.T) http.Handler {
	t.Helper()
	t.Setenv("AM_HOME", t.TempDir())
	t.Setenv("ANTHROPIC_API_KEY", "") // don't inherit the dev's real env

	rot := NewRotator("claude")
	life := NewLifecycle()
	mode := &ProxyMode{}
	pool := router.NewAccountPoolRouter([]types.ProviderAdapter{&stubAdapter{id: "stub"}})
	rp := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unexpected reverse-proxy call in test", http.StatusTeapot)
	})
	sw := &swappableHandler{}
	h := newHandler(rot, life, mode, pool, pool, rp, "https://api.anthropic.com", sw, func() {})
	sw.Set(h)
	return sw
}

func TestHandler_SessionRejectsMissingPID(t *testing.T) {
	h := newTestHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/_am/session?event=start", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing pid, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandler_SessionRejectsZeroPID(t *testing.T) {
	h := newTestHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/_am/session?pid=0&event=start", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for pid=0, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandler_SessionStartEnd(t *testing.T) {
	h := newTestHandler(t)
	pid := os.Getpid() // guaranteed alive, so pruneDead() won't reap it out from under us

	start := httptest.NewRequest(http.MethodPost, "/_am/session?pid="+strconv.Itoa(pid)+"&event=start", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, start)
	if rec.Code != http.StatusOK {
		t.Fatalf("start: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := strings.TrimSpace(rec.Body.String()); got != "1" {
		t.Errorf("start: expected session count 1, got %q", got)
	}

	end := httptest.NewRequest(http.MethodPost, "/_am/session?pid="+strconv.Itoa(pid)+"&event=end", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, end)
	if rec.Code != http.StatusOK {
		t.Fatalf("end: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := strings.TrimSpace(rec.Body.String()); got != "0" {
		t.Errorf("end: expected session count 0, got %q", got)
	}
}

// TestHandler_SessionLegacyOpParam locks in the "op=" fallback added for
// the event/op rename rolling-upgrade risk: a client built against the old
// param name must still register against a handler built from the current
// code.
func TestHandler_SessionLegacyOpParam(t *testing.T) {
	h := newTestHandler(t)
	pid := os.Getpid()

	req := httptest.NewRequest(http.MethodPost, "/_am/session?pid="+strconv.Itoa(pid)+"&op=start", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := strings.TrimSpace(rec.Body.String()); got != "1" {
		t.Errorf("expected legacy op=start to register a session, got %q", got)
	}
}

func TestHandler_SwitchProvider(t *testing.T) {
	h := newTestHandler(t)

	req := httptest.NewRequest(http.MethodPost, "/_am/switch-provider?to=stub", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["mode"] != "provider" || resp["active"] != "stub" {
		t.Errorf("expected mode=provider active=stub, got %+v", resp)
	}

	// The mode switch must be visible through /_am/status too.
	statusReq := httptest.NewRequest(http.MethodGet, "/_am/status", nil)
	statusRec := httptest.NewRecorder()
	h.ServeHTTP(statusRec, statusReq)
	var status map[string]any
	if err := json.Unmarshal(statusRec.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if status["mode"] != "provider" {
		t.Errorf("expected status mode=provider, got %v", status["mode"])
	}
}

func TestHandler_Pool(t *testing.T) {
	h := newTestHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/_am/pool", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Providers []map[string]any `json:"providers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Providers) != 1 || resp.Providers[0]["id"] != "stub" {
		t.Errorf("expected the stub adapter listed in pool status, got %+v", resp.Providers)
	}
}

// TestHandler_ChatCompletionsRoutesToPool is a regression test for the new
// OpenAI-compatible gateway: /v1/chat/completions must reach the pool
// router (bridge.HandleChatCompletions), not the Anthropic reverse proxy.
func TestHandler_ChatCompletionsRoutesToPool(t *testing.T) {
	h := newTestHandler(t)
	body := `{"model":"test","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code == http.StatusTeapot {
		t.Fatalf("request reached the reverse proxy instead of the pool router")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 from the pool-backed gateway, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestHandler_MessagesFallsBackToPoolWhenNoClaudeAuth is the key backward-
// compatibility/regression test: with no rotator token, no X-Api-Key
// header, and no ANTHROPIC_API_KEY env var, the old /v1/messages endpoint
// must still work by falling back to the provider pool instead of hitting
// (and failing against) the Anthropic reverse proxy.
func TestHandler_MessagesFallsBackToPoolWhenNoClaudeAuth(t *testing.T) {
	h := newTestHandler(t)
	body := `{"model":"claude-3-5-sonnet-20241022","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code == http.StatusTeapot {
		t.Fatalf("expected pool fallback, request reached the reverse-proxy stub instead")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 from the pool fallback, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestHandler_MessagesUsesReverseProxyWhenAPIKeyPresent locks in the
// original (pre-refactor) behavior: when there IS a usable credential
// (here, an explicit X-Api-Key header), /v1/messages must still go through
// the same Anthropic reverse-proxy path it always did — the new pool/bridge
// code must not have hijacked that case.
func TestHandler_MessagesUsesReverseProxyWhenAPIKeyPresent(t *testing.T) {
	h := newTestHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	req.Header.Set("X-Api-Key", "sk-test-key")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusTeapot {
		t.Errorf("expected the reverse-proxy path (stub returns 418), got %d: %s", rec.Code, rec.Body.String())
	}
}

func newHandlerWithRotator(t *testing.T, rot *Rotator, poolAdapters []types.ProviderAdapter) (http.Handler, *httptest.ResponseRecorder) {
	t.Helper()
	t.Setenv("AM_HOME", t.TempDir())
	t.Setenv("ANTHROPIC_API_KEY", "")
	life := NewLifecycle()
	mode := &ProxyMode{}
	pool := router.NewAccountPoolRouter(poolAdapters)
	rp := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "reverse-proxy", http.StatusTeapot)
	})
	sw := &swappableHandler{}
	h := newHandler(rot, life, mode, pool, pool, rp, "https://api.anthropic.com", sw, func() {})
	sw.Set(h)
	return sw, nil
}

// Sole Claude Code account on cooldown → provider pool (API→web→DDG).
func TestHandler_SingleClaudeCoolingFailsOverToPool(t *testing.T) {
	rot := &Rotator{
		tool:           "claude",
		order:          []string{"solo"},
		tokens:         map[string]*types.Token{"solo": {Access: "tok"}},
		accounts:       map[string]string{},
		cooldown:       map[string]time.Time{"solo": time.Now().Add(time.Hour)},
		dead:           map[string]bool{},
		autoSwitches:   map[string]int{},
		manualSwitches: map[string]int{},
		usedThreshold:  DefaultUsedThreshold,
	}
	h, _ := newHandlerWithRotator(t, rot, []types.ProviderAdapter{&stubAdapter{id: "stub"}})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(
		`{"model":"claude-3-5-sonnet-20241022","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code == http.StatusTeapot {
		t.Fatal("expected pool failover, hit reverse-proxy instead")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 from pool, got %d: %s", rec.Code, rec.Body.String())
	}
}

// All Claude Code accounts on cooldown → provider pool (API→web→DDG).
func TestHandler_AllClaudeCoolingFailsOverToPool(t *testing.T) {
	rot := &Rotator{
		tool:   "claude",
		order:  []string{"a", "b"},
		tokens: map[string]*types.Token{"a": {Access: "tok-a"}, "b": {Access: "tok-b"}},
		accounts: map[string]string{},
		cooldown: map[string]time.Time{
			"a": time.Now().Add(time.Hour),
			"b": time.Now().Add(time.Hour),
		},
		dead:           map[string]bool{},
		autoSwitches:   map[string]int{},
		manualSwitches: map[string]int{},
		usedThreshold:  DefaultUsedThreshold,
	}
	h, _ := newHandlerWithRotator(t, rot, []types.ProviderAdapter{&stubAdapter{id: "stub"}})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(
		`{"model":"claude-3-5-sonnet-20241022","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code == http.StatusTeapot {
		t.Fatal("expected pool failover when all Claude cooling, hit reverse-proxy instead")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 from pool, got %d: %s", rec.Code, rec.Body.String())
	}
}

// Exhausted Claude with empty tool pool → stay on Anthropic reverse-proxy.
func TestHandler_ClaudeFailoverEmptyPoolUsesReverseProxy(t *testing.T) {
	t.Setenv("AM_HOME", t.TempDir())
	t.Setenv("ANTHROPIC_API_KEY", "")

	rot := &Rotator{
		tool:           "claude",
		order:          []string{"solo"},
		tokens:         map[string]*types.Token{"solo": {Access: "tok"}},
		accounts:       map[string]string{},
		cooldown:       map[string]time.Time{"solo": time.Now().Add(time.Hour)},
		dead:           map[string]bool{},
		autoSwitches:   map[string]int{},
		manualSwitches: map[string]int{},
		usedThreshold:  DefaultUsedThreshold,
	}
	life := NewLifecycle()
	mode := &ProxyMode{}
	empty := router.NewAccountPoolRouter(nil)
	rp := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "reverse-proxy", http.StatusTeapot)
	})
	sw := &swappableHandler{}
	h := newHandler(rot, life, mode, empty, empty, rp, "https://api.anthropic.com", sw, func() {})
	sw.Set(h)

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusTeapot {
		t.Fatalf("expected Anthropic reverse-proxy, got %d: %s", rec.Code, rec.Body.String())
	}
}

// After a Claude cooldown expires, next request returns to Anthropic reverse-proxy.
func TestHandler_ClaudeResetSwitchesBackFromPool(t *testing.T) {
	rot := &Rotator{
		tool:   "claude",
		order:  []string{"a", "b"},
		idx:    0,
		tokens: map[string]*types.Token{"a": {Access: "tok-a"}, "b": {Access: "tok-b"}},
		accounts: map[string]string{},
		cooldown: map[string]time.Time{
			"a": time.Now().Add(time.Hour), // still cooling
			// b has no cooldown → available again
		},
		dead:           map[string]bool{},
		autoSwitches:   map[string]int{},
		manualSwitches: map[string]int{},
		usedThreshold:  DefaultUsedThreshold,
	}
	h, _ := newHandlerWithRotator(t, rot, []types.ProviderAdapter{&stubAdapter{id: "stub"}})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusTeapot {
		t.Fatalf("expected reverse-proxy after Claude reset, got %d: %s", rec.Code, rec.Body.String())
	}
	if rot.Active() != "b" {
		t.Fatalf("expected auto-switch back to b, active=%q", rot.Active())
	}
}
