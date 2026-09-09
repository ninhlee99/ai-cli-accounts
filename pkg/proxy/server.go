package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"amux-accounts/pkg/bridge"
	"amux-accounts/pkg/profile"
	"amux-accounts/pkg/provider"
	"amux-accounts/pkg/router"
	"amux-accounts/pkg/types"
	"amux-accounts/pkg/usage"
)

type ProxyMode struct {
	mu   sync.Mutex
	mode string // "claude" | "provider"
}

func (m *ProxyMode) Get() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.mode == "" {
		return "claude"
	}
	return m.mode
}

func (m *ProxyMode) Set(v string) {
	m.mu.Lock()
	m.mode = v
	m.mu.Unlock()
}

// swappableHandler lets /_am/shutdown swap in a stripped-down passthrough
// handler (see passthrough.go) for a brief grace window before the socket
// actually closes, without needing to rebind the port — the *http.Server
// always points at one of these, and only its target changes.
type swappableHandler struct {
	mu sync.RWMutex
	h  http.Handler
}

func (s *swappableHandler) Set(h http.Handler) {
	s.mu.Lock()
	s.h = h
	s.mu.Unlock()
}

func (s *swappableHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	h := s.h
	s.mu.RUnlock()
	h.ServeHTTP(w, r)
}

// shutdownGrace is how long /_am/shutdown keeps serving Anthropic-direct
// (see swappableHandler, newPassthroughHandler) after being asked to stop,
// when at least one claude session is still attached — long enough for a
// request already in flight to land somewhere real instead of getting
// connection-refused, short enough that `am proxy down` doesn't hang.
const shutdownGrace = 3 * time.Second

// RunProxy starts the server on the given address, serving Claude Code,
// OpenAI gateway, and administrative endpoints.
func RunProxy(addr, upstream string) error {
	if addr == "" {
		addr = "127.0.0.1:8787"
	}
	if upstream == "" {
		upstream = "https://api.anthropic.com"
	}

	profile.SyncActiveFromSystem("claude")

	rot := NewRotator("claude")
	go rot.PeriodicSnapshot()
	life := NewLifecycle()
	mode := &ProxyMode{}

	adapters, _ := provider.LoadAccounts(provider.DefaultAccountsPath())
	pool := router.NewAccountPoolRouter(adapters)

	rp, err := newReverseProxy(upstream, rot)
	if err != nil {
		return err
	}

	sw := &swappableHandler{}
	var srv *http.Server
	handler := newHandler(rot, life, mode, pool, rp, upstream, sw, func() {
		if srv != nil {
			_ = srv.Close()
		}
	})
	sw.Set(handler)
	srv = &http.Server{Addr: addr, Handler: sw}

	_ = os.MkdirAll(types.BaseDir(), 0o700)
	log.Printf("amux proxy up on %s, active claude account %q", addr, rot.Active())
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// newReverseProxy builds the httputil.ReverseProxy that forwards to the real
// Anthropic API (or whatever `upstream` points at), injecting either the
// rotator's OAuth token or a caller-supplied API key.
func newReverseProxy(upstream string, rot *Rotator) (*httputil.ReverseProxy, error) {
	target, err := url.Parse(upstream)
	if err != nil {
		return nil, fmt.Errorf("invalid upstream url %s: %w", upstream, err)
	}

	return &httputil.ReverseProxy{
		Director: func(r *http.Request) {
			r.URL.Scheme = target.Scheme
			r.URL.Host = target.Host
			r.Host = target.Host
			tok := rot.Token()
			if tok != "" {
				r.Header.Set("Authorization", "Bearer "+tok)
				r.Header.Del("X-Api-Key")
				if !strings.Contains(r.Header.Get("anthropic-beta"), "oauth") {
					r.Header.Add("anthropic-beta", "oauth-2025-04-20")
				}
			} else {
				apiKey := r.Header.Get("X-Api-Key")
				if apiKey == "" {
					if envKey := os.Getenv("ANTHROPIC_API_KEY"); envKey != "" {
						apiKey = envKey
						r.Header.Set("X-Api-Key", apiKey)
					}
				}
				if r.Header.Get("X-Api-Key") != "" {
					r.Header.Del("Authorization")
				}
			}
		},
		ModifyResponse: func(resp *http.Response) error {
			rot.Observe(resp)
			usage.WrapUsageCapture(resp, rot.Active())
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("proxy error: %v", err)
			http.Error(w, "amux proxy: upstream error", http.StatusBadGateway)
		},
	}, nil
}

// newHandler builds the full HTTP handler serving Claude Code, the OpenAI
// gateway, and the /_am/ admin endpoints. Split out from RunProxy so it can
// be exercised directly with httptest (no live listener, no goroutine)
// instead of only being reachable through a real server socket — see
// server_test.go. `shutdown` is called by /_am/shutdown instead of closing
// an *http.Server directly, since the server that wraps this handler
// doesn't exist yet when the handler is built (RunProxy ties the two
// together via a closure).
func newHandler(rot *Rotator, life *Lifecycle, mode *ProxyMode, pool *router.AccountPoolRouter, rp http.Handler, upstream string, sw *swappableHandler, shutdown func()) http.Handler {
	mux := http.NewServeMux()

	// 1. OpenAI Standard Gateway
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		bridge.HandleChatCompletions(w, r, pool)
	})
	mux.HandleFunc("/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		bridge.HandleChatCompletions(w, r, pool)
	})
	mux.HandleFunc("/v1/models", bridge.HandleModels)
	mux.HandleFunc("/models", bridge.HandleModels)

	// 2. Admin / Monitoring Endpoints
	mux.HandleFunc("/_am/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		s := rot.Status()
		s["sessions"] = life.Sessions()
		s["upstream"] = upstream
		s["mode"] = mode.Get()
		s["pool"] = pool.Status()
		_ = json.NewEncoder(w).Encode(s)
	})

	mux.HandleFunc("/_am/switch", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("to")
		if name == "" {
			http.Error(w, "missing 'to'", http.StatusBadRequest)
			return
		}
		if err := rot.ForceSwitch(name); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mode.Set("claude")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"active": rot.Active(), "mode": "claude"})
	})

	mux.HandleFunc("/_am/switch-provider", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("to")
		pool.SetPreferred(name)
		mode.Set("provider")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"active": name, "mode": "provider"})
	})

	mux.HandleFunc("/_am/session", func(w http.ResponseWriter, r *http.Request) {
		pid, err := strconv.Atoi(r.URL.Query().Get("pid"))
		if err != nil || pid <= 0 {
			// Reject rather than silently tracking pid=0: pruneDead() below
			// can never reap it (see lifecycle.go — kill(0, sig) targets the
			// caller's whole process group and always succeeds), so an
			// invalid pid here would otherwise create an immortal phantom
			// session that inflates `am status` and can permanently block
			// `am proxy down --force`'s attached-session check.
			http.Error(w, "invalid or missing 'pid'", http.StatusBadRequest)
			return
		}
		// "event" is the current param name; "op" is what the proxy spoke
		// before the SessionStart/SessionEnd rename (see old proxy.go). A
		// background `am proxy` process is long-lived and isn't restarted
		// just because the `am` binary on disk was upgraded, so an
		// old-server-new-client mismatch is a real rolling-upgrade case, not
		// just theoretical — accept either so an already-running old-code
		// proxy and a freshly-built client (or vice versa) still agree.
		event := r.URL.Query().Get("event")
		if event == "" {
			event = r.URL.Query().Get("op")
		}
		switch event {
		case "start":
			life.AddSession(pid)
		case "end":
			life.EndSession(pid)
		}
		fmt.Fprintf(w, "%d\n", life.Sessions())
	})

	mux.HandleFunc("/_am/pool", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"preferred": pool.Preferred(),
			"providers": pool.Status(),
		})
	})

	mux.HandleFunc("/_am/sync", func(w http.ResponseWriter, r *http.Request) {
		profile.SyncActiveFromSystem("claude")
		rot.RefreshFromDisk()
		if reloaded, err := provider.LoadAccounts(provider.DefaultAccountsPath()); err == nil {
			pool.Reload(reloaded)
		}
		fmt.Fprintf(w, "%d\n", len(rot.Names()))
	})

	mux.HandleFunc("/_am/shutdown", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
		go func() {
			if life.Sessions() > 0 {
				// Hand off to Anthropic-direct passthrough for a short
				// grace window instead of yanking the socket out from
				// under an attached claude session — see swappableHandler
				// and shutdownGrace above.
				if ph, err := newPassthroughHandler(rot, life, upstream, false, nil); err == nil {
					sw.Set(ph)
				} else {
					log.Printf("amux proxy: grace-drain passthrough unavailable: %v", err)
				}
				time.Sleep(shutdownGrace)
			} else {
				time.Sleep(100 * time.Millisecond)
			}
			shutdown()
		}()
	})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		// Admin routes
		if strings.HasPrefix(path, "/_am/") {
			mux.ServeHTTP(w, r)
			return
		}

		// OpenAI routes
		if strings.HasSuffix(path, "/chat/completions") || path == "/v1/models" || path == "/models" {
			mux.ServeHTTP(w, r)
			return
		}

		// Anthropic messages endpoint: /v1/messages
		if strings.HasSuffix(path, "/messages") {
			if mode.Get() == "provider" {
				body, err := io.ReadAll(io.LimitReader(r.Body, 10<<20))
				if err != nil {
					http.Error(w, "read request body: "+err.Error(), http.StatusBadRequest)
					return
				}
				if err := bridge.HandleClaudeMessages(w, r, pool, body); err != nil {
					log.Printf("bridge /v1/messages error: %v", err)
				}
				return
			}

			// In "claude" mode: if we have a valid token or explicit API key (header or env):
			if rot.Token() != "" || r.Header.Get("X-Api-Key") != "" || os.Getenv("ANTHROPIC_API_KEY") != "" {
				rp.ServeHTTP(w, r)
				return
			}

			// If no Claude accounts are active, bridge to pool
			body, err := io.ReadAll(io.LimitReader(r.Body, 10<<20))
			if err != nil {
				http.Error(w, "read request body: "+err.Error(), http.StatusBadRequest)
				return
			}
			if err := bridge.HandleClaudeMessages(w, r, pool, body); err != nil {
				log.Printf("bridge /v1/messages fallback error: %v", err)
			}
			return
		}

		// Fallback reverse proxy
		rp.ServeHTTP(w, r)
	})
}
