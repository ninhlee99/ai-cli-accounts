package main

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
)

// cmdProxy runs a local reverse proxy in front of api.anthropic.com. It injects
// the OAuth access token of the *active* Claude profile, refreshes that token
// when it is about to expire, and rotates to the next profile when the current
// account's unified rate limit is nearly exhausted or returns HTTP 429 — so the
// client (Claude Code) never sees an interruption mid-task.
func cmdProxy(args []string) {
	addr := "127.0.0.1:8787"
	upstream := "https://api.anthropic.com"
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--addr":
			i++
			addr = args[i]
		case "--upstream":
			i++
			upstream = args[i]
		}
	}
	profs := listProfiles("claude")
	if len(profs) == 0 {
		die("no claude profiles saved; run `am save claude <name>` for each account first")
	}

	rot := &rotator{tool: "claude"}
	rot.load()
	if rot.active() == "" {
		rot.setActive(profs[0].Name)
	}

	target, _ := url.Parse(upstream)
	rp := &httputil.ReverseProxy{
		Director: func(r *http.Request) {
			r.URL.Scheme = target.Scheme
			r.URL.Host = target.Host
			r.Host = target.Host
			tok := rot.token()
			r.Header.Set("Authorization", "Bearer "+tok)
			r.Header.Del("X-Api-Key")
			// Claude Code's OAuth path expects this beta flag.
			if !strings.Contains(r.Header.Get("anthropic-beta"), "oauth") {
				r.Header.Add("anthropic-beta", "oauth-2025-04-20")
			}
		},
		ModifyResponse: func(resp *http.Response) error {
			rot.observe(resp)
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("proxy error: %v", err)
			http.Error(w, "am proxy: upstream error", http.StatusBadGateway)
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/_am/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(rot.status())
	})
	mux.Handle("/", rp)

	fmt.Printf("am proxy on http://%s -> %s\n", addr, upstream)
	fmt.Printf("active claude profile: %s   (rotation order: %s)\n", rot.active(), strings.Join(rot.names(), " -> "))
	fmt.Printf("\npoint Claude Code at it:\n  export ANTHROPIC_BASE_URL=http://%s\n  export ANTHROPIC_AUTH_TOKEN=am-proxy   # any non-empty value\n\n", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

// rotator holds the in-memory token state for the proxy.
type rotator struct {
	tool string

	mu        sync.Mutex
	order     []string          // profile names, rotation order
	idx       int               // index into order
	tokens    map[string]*token // profile name -> live token
	cooldown  map[string]time.Time
	switches  int
	lastSwitch time.Time
}

type token struct {
	Access       string
	Refresh      string
	ExpiresAt    time.Time
	remaining    float64 // last seen unified-remaining fraction/count (-1 unknown)
	resetAt      time.Time
}

const (
	// rotate when the account reports this fraction (or fewer) of its window left
	rotateThreshold = 0.06
	// don't return to an account that hit a limit until this long after its reset
	cooldownPad = 30 * time.Second
	// refresh an access token this long before it actually expires
	refreshLead = 2 * time.Minute
)

func (r *rotator) load() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tokens = map[string]*token{}
	r.cooldown = map[string]time.Time{}
	for _, p := range listProfiles(r.tool) {
		r.order = append(r.order, p.Name)
		r.tokens[p.Name] = loadClaudeToken(r.tool, p.Name)
	}
	if a := readActivePointer(r.tool); a != "" {
		for i, n := range r.order {
			if n == a {
				r.idx = i
			}
		}
	}
}

func (r *rotator) names() []string { return r.order }
func (r *rotator) active() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.order) == 0 {
		return ""
	}
	return r.order[r.idx]
}
func (r *rotator) setActive(name string) {
	r.mu.Lock()
	for i, n := range r.order {
		if n == name {
			r.idx = i
		}
	}
	r.mu.Unlock()
	writeActivePointer(r.tool, name)
}

// token returns a valid access token for the active profile, refreshing if due.
func (r *rotator) token() string {
	r.mu.Lock()
	name := r.order[r.idx]
	t := r.tokens[name]
	r.mu.Unlock()

	if t == nil {
		return ""
	}
	if time.Until(t.ExpiresAt) < refreshLead && t.Refresh != "" {
		if nt, err := refreshClaudeToken(t.Refresh); err == nil {
			r.mu.Lock()
			t.Access, t.Refresh, t.ExpiresAt = nt.Access, nt.Refresh, nt.ExpiresAt
			r.mu.Unlock()
			persistClaudeToken(r.tool, name, t)
			log.Printf("[%s] refreshed access token (exp %s)", name, t.ExpiresAt.Format(time.Kitchen))
		} else {
			log.Printf("[%s] token refresh failed: %v", name, err)
		}
	}
	return t.Access
}

// observe reads rate-limit headers off each response and rotates if needed.
func (r *rotator) observe(resp *http.Response) {
	r.mu.Lock()
	name := r.order[r.idx]
	t := r.tokens[name]
	r.mu.Unlock()

	h := resp.Header
	rem, remOK := parseFirstFloat(h,
		"anthropic-ratelimit-unified-remaining",
		"anthropic-ratelimit-unified-5h-remaining",
		"anthropic-ratelimit-requests-remaining",
	)
	lim, limOK := parseFirstFloat(h,
		"anthropic-ratelimit-unified-limit",
		"anthropic-ratelimit-unified-5h-limit",
		"anthropic-ratelimit-requests-limit",
	)
	if reset := parseFirstTime(h,
		"anthropic-ratelimit-unified-reset",
		"anthropic-ratelimit-unified-5h-reset",
		"anthropic-ratelimit-requests-reset",
	); !reset.IsZero() && t != nil {
		r.mu.Lock()
		t.resetAt = reset
		r.mu.Unlock()
	}

	frac := -1.0
	if remOK && limOK && lim > 0 {
		frac = rem / lim
	} else if remOK {
		frac = rem // count-only; treat small absolute as low
	}
	if t != nil {
		r.mu.Lock()
		t.remaining = frac
		r.mu.Unlock()
	}

	hardLimited := resp.StatusCode == http.StatusTooManyRequests
	nearLimit := (remOK && limOK && lim > 0 && frac <= rotateThreshold) ||
		(remOK && !limOK && rem <= 2)

	if hardLimited || nearLimit {
		reason := "near limit"
		if hardLimited {
			reason = "429"
		}
		r.rotate(name, reason)
	}
}

func (r *rotator) rotate(from, reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.order[r.idx] != from {
		return // already moved on
	}
	if t := r.tokens[from]; t != nil && !t.resetAt.IsZero() {
		r.cooldown[from] = t.resetAt.Add(cooldownPad)
	} else {
		r.cooldown[from] = time.Now().Add(15 * time.Minute)
	}
	n := len(r.order)
	for step := 1; step <= n; step++ {
		cand := r.order[(r.idx+step)%n]
		if cd, ok := r.cooldown[cand]; ok && time.Now().Before(cd) {
			continue
		}
		r.idx = (r.idx + step) % n
		r.switches++
		r.lastSwitch = time.Now()
		writeActivePointer(r.tool, cand)
		log.Printf("ROTATE (%s): %s -> %s", reason, from, cand)
		return
	}
	log.Printf("ROTATE (%s): %s -> (all accounts cooling down; staying)", reason, from)
}

func (r *rotator) status() map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	accts := []map[string]any{}
	for _, n := range r.order {
		t := r.tokens[n]
		m := map[string]any{"profile": n, "active": n == r.order[r.idx]}
		if t != nil {
			m["remaining"] = t.remaining
			m["token_expires"] = t.ExpiresAt
			if !t.resetAt.IsZero() {
				m["limit_reset"] = t.resetAt
			}
		}
		if cd, ok := r.cooldown[n]; ok && time.Now().Before(cd) {
			m["cooldown_until"] = cd
		}
		accts = append(accts, m)
	}
	return map[string]any{
		"tool":        r.tool,
		"switches":    r.switches,
		"last_switch": r.lastSwitch,
		"accounts":    accts,
	}
}

func parseFirstFloat(h http.Header, keys ...string) (float64, bool) {
	for _, k := range keys {
		if v := h.Get(k); v != "" {
			if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
				return f, true
			}
		}
	}
	return 0, false
}

func parseFirstTime(h http.Header, keys ...string) time.Time {
	for _, k := range keys {
		v := strings.TrimSpace(h.Get(k))
		if v == "" {
			continue
		}
		if secs, err := strconv.ParseInt(v, 10, 64); err == nil {
			// could be epoch seconds or delta seconds
			if secs > 1_000_000_000 {
				return time.Unix(secs, 0)
			}
			return time.Now().Add(time.Duration(secs) * time.Second)
		}
		if ts, err := time.Parse(time.RFC3339, v); err == nil {
			return ts
		}
	}
	return time.Time{}
}

// cmdRun execs a tool with env pointed at a running proxy.
func cmdRun(tool string, rest []string) {
	addr := envOr("AM_PROXY_ADDR", "127.0.0.1:8787")
	base := "http://" + addr
	env := os.Environ()
	switch tool {
	case "claude":
		env = append(env,
			"ANTHROPIC_BASE_URL="+base,
			"ANTHROPIC_AUTH_TOKEN=am-proxy",
			"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
		)
	default:
		die("am run currently supports: claude")
	}
	// Fail fast if the proxy isn't up.
	if _, err := http.Get(base + "/_am/status"); err != nil {
		die("proxy not reachable at %s (start it with: am proxy)", base)
	}
	bin, err := lookPath(tool)
	if err != nil {
		die("%v", err)
	}
	execProcess(bin, append([]string{tool}, rest...), env)
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func fmtFrac(f float64) string {
	if f < 0 {
		return "?"
	}
	return fmt.Sprintf("%.0f%%", f*100)
}

var _ = io.Discard
