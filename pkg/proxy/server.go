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

	"ai-cli-accounts/pkg/bridge"
	"ai-cli-accounts/pkg/profile"
	"ai-cli-accounts/pkg/provider"
	"ai-cli-accounts/pkg/router"
	"ai-cli-accounts/pkg/types"
	"ai-cli-accounts/pkg/usage"
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

	adapters, err := provider.LoadAccounts(provider.DefaultAccountsPath())
	if err != nil || len(adapters) == 0 {
		adapters = []types.ProviderAdapter{
			&provider.DuckDuckGoAdapter{
				AdapterID:   "duckduckgo",
				TargetModel: "claude-3-haiku-20240307",
				PriorityLvl: 99,
			},
		}
	}
	pool := router.NewAccountPoolRouter(adapters)

	target, err := url.Parse(upstream)
	if err != nil {
		return fmt.Errorf("invalid upstream url %s: %w", upstream, err)
	}

	rp := &httputil.ReverseProxy{
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
			http.Error(w, "am proxy: upstream error", http.StatusBadGateway)
		},
	}

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
		pid, _ := strconv.Atoi(r.URL.Query().Get("pid"))
		ev := r.URL.Query().Get("event")
		switch ev {
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

	var srv *http.Server
	mux.HandleFunc("/_am/shutdown", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
		go func() {
			time.Sleep(100 * time.Millisecond)
			if srv != nil {
				_ = srv.Close()
			}
		}()
	})

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

	srv = &http.Server{Addr: addr, Handler: handler}

	_ = os.MkdirAll(types.BaseDir(), 0o700)
	log.Printf("am proxy up on %s, active claude account %q", addr, rot.Active())
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}
