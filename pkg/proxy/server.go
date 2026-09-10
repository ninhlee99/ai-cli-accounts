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
	handler := newHandler(rot, life, mode, pool, pool, rp, upstream, sw, func() {
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
// gateway, and the /_am/ admin endpoints.
//
// chatPool serves /v1/chat/completions; toolPool serves Claude/Codex
// /v1/messages failover. Callers may pass the same router for both.
func newHandler(rot *Rotator, life *Lifecycle, mode *ProxyMode, chatPool, toolPool *router.AccountPoolRouter, rp http.Handler, upstream string, sw *swappableHandler, shutdown func()) http.Handler {
	if toolPool == nil {
		toolPool = chatPool
	}
	mux := http.NewServeMux()

	// 1. OpenAI Standard Gateway
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		bridge.HandleChatCompletions(w, r, chatPool)
	})
	mux.HandleFunc("/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		bridge.HandleChatCompletions(w, r, chatPool)
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
		s["pool"] = chatPool.Status()
		s["tool_pool"] = toolPool.Status()
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
		chatPool.SetPreferred(name)
		toolPool.SetPreferred(name)
		mode.Set("provider")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"active": name, "mode": "provider"})
	})

	mux.HandleFunc("/_am/session", func(w http.ResponseWriter, r *http.Request) {
		pid, err := strconv.Atoi(r.URL.Query().Get("pid"))
		if err != nil || pid <= 0 {
			http.Error(w, "invalid or missing 'pid'", http.StatusBadRequest)
			return
		}
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
			"preferred":  chatPool.Preferred(),
			"providers":  chatPool.Status(),
			"tool_pool":  toolPool.Status(),
		})
	})

	mux.HandleFunc("/_am/sync", func(w http.ResponseWriter, r *http.Request) {
		profile.SyncActiveFromSystem("claude")
		rot.RefreshFromDisk()
		if reloaded, err := provider.LoadAccounts(provider.DefaultAccountsPath()); err == nil {
			chatPool.Reload(reloaded)
			if toolPool != chatPool {
				toolPool.Reload(reloaded)
			}
		}
		fmt.Fprintf(w, "%d\n", len(rot.Names()))
	})

	mux.HandleFunc("/_am/shutdown", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
		go func() {
			if life.Sessions() > 0 {
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

		if strings.HasPrefix(path, "/_am/") {
			mux.ServeHTTP(w, r)
			return
		}

		if strings.HasSuffix(path, "/chat/completions") || path == "/v1/models" || path == "/models" {
			mux.ServeHTTP(w, r)
			return
		}

		// Anthropic messages (/v1/messages) — Claude Code / tool clients.
		if strings.HasSuffix(path, "/messages") {
			hasAPIKey := r.Header.Get("X-Api-Key") != "" || os.Getenv("ANTHROPIC_API_KEY") != ""

			usePool := false
			pool := toolPool
			autoFromClaude := false
			switch {
			case mode.Get() == "provider":
				usePool = true
			case hasAPIKey:
				usePool = false
			case rot.ShouldFailoverToProviderPool():
				usePool = toolPool.Len() > 0
				if usePool {
					autoFromClaude = true
					log.Printf("amux: all Claude accounts unavailable — failover to provider pool (API→web)")
				}
			case rot.Token() != "" || rot.ProfileCount() > 0:
				if rot.ProfileCount() > 0 {
					rot.EnsureUsableActive()
				}
				usePool = rot.Token() == "" && !hasAPIKey
			default:
				usePool = toolPool.Len() > 0
			}

			if usePool {
				body, err := io.ReadAll(io.LimitReader(r.Body, 10<<20))
				if err != nil {
					http.Error(w, "read request body: "+err.Error(), http.StatusBadRequest)
					return
				}
				if err := bridge.HandleClaudeMessages(w, r, pool, body); err != nil {
					log.Printf("bridge /v1/messages error: %v", err)
					return
				}
				// Claude→pool auto-failover: flip mode + preferred so status
				// shows the provider that actually answered (● active).
				if autoFromClaude {
					if id := pool.LastUsed(); id != "" {
						pool.SetPreferred(id)
						mode.Set("provider")
						log.Printf("amux: auto-switched active provider → %s (status updated)", id)
					}
				}
				return
			}

			rp.ServeHTTP(w, r)
			return
		}

		rp.ServeHTTP(w, r)
	})
}
