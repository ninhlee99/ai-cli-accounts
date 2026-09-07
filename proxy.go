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
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const defaultAddr = "127.0.0.1:8787"

func proxyAddr() string { return envOr("AM_PROXY_ADDR", defaultAddr) }
func proxyBase() string { return "http://" + proxyAddr() }

// cmdProxy dispatches: `am proxy` runs the reverse proxy in the foreground;
// `am proxy up` / `am proxy down` are what the Claude Code hooks call to start
// it on demand and stop it when the last session ends.
func cmdProxy(args []string) {
	if len(args) > 0 {
		switch args[0] {
		case "up":
			proxyEnsureUp()
			return
		case "down":
			proxyReleaseAndMaybeStop()
			return
		}
	}
	runProxyForeground(args)
}

// runProxyForeground is the actual server. It exits on its own once no Claude
// session has held it for idleShutdown, so it never lingers after `claude`.
func runProxyForeground(args []string) {
	addr := defaultAddr
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
	syncActiveFromSystem("claude")

	if len(listProfiles("claude")) == 0 {
		die("no claude profiles saved; log into Claude, then run `am save claude`")
	}

	rot := &rotator{tool: "claude"}
	rot.load()
	if rot.active() == "" {
		rot.setActive(listProfiles("claude")[0].Name)
	}

	life := &lifecycle{}
	life.touch()

	target, _ := url.Parse(upstream)
	rp := &httputil.ReverseProxy{
		Director: func(r *http.Request) {
			life.touch()
			r.URL.Scheme = target.Scheme
			r.URL.Host = target.Host
			r.Host = target.Host
			r.Header.Set("Authorization", "Bearer "+rot.token())
			r.Header.Del("X-Api-Key")
			if !strings.Contains(r.Header.Get("anthropic-beta"), "oauth") {
				r.Header.Add("anthropic-beta", "oauth-2025-04-20")
			}
			if os.Getenv("AM_PROXY_DEBUG") != "" {
				log.Printf("%s %s -> %s", r.Method, r.URL.Path, rot.active())
			}
		},
		ModifyResponse: func(resp *http.Response) error { rot.observe(resp); return nil },
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("proxy error: %v", err)
			http.Error(w, "am proxy: upstream error", http.StatusBadGateway)
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/_am/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(rot.status())
	})
	mux.HandleFunc("/_am/switch", func(w http.ResponseWriter, r *http.Request) {
		if err := rot.forceSwitch(r.URL.Query().Get("to")); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"active": rot.active()})
	})
	// Hooks register/deregister a session; the proxy shuts down shortly after
	// the count hits zero (grace period covers a quick claude restart).
	mux.HandleFunc("/_am/session", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("op") {
		case "start":
			life.addSession()
		case "end":
			life.endSession()
		}
		fmt.Fprintf(w, "%d\n", life.sessions())
	})
	// SessionStart hits this on every `claude` launch, not just the one that
	// spawned the proxy — so a login done since the proxy came up (e.g. logged
	// into a second account, then opened a new claude tab) gets snapshotted
	// and pulled into rotation without needing `am add` or a proxy restart.
	mux.HandleFunc("/_am/sync", func(w http.ResponseWriter, r *http.Request) {
		syncActiveFromSystem("claude")
		rot.refreshFromDisk()
		fmt.Fprintf(w, "%d\n", len(rot.names()))
	})

	srv := &http.Server{Addr: addr, Handler: withProxy(mux, rp)}
	go life.watch(srv)

	_ = os.MkdirAll(baseDir(), 0o700)
	log.Printf("am proxy up on %s, account %q", addr, rot.active())
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
	log.Printf("am proxy stopped")
}

func withProxy(mux *http.ServeMux, rp http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/_am/") {
			mux.ServeHTTP(w, r)
			return
		}
		rp.ServeHTTP(w, r)
	})
}

// lifecycle tracks active Claude sessions and the last request time so the
// proxy can exit when nothing needs it.
type lifecycle struct {
	mu       sync.Mutex
	nSess    int
	lastSeen time.Time
}

const (
	// With no session registered, stop soon after the last request (the grace
	// covers a quick `claude` restart between SessionEnd and SessionStart).
	zeroGrace = 30 * time.Second
	// With a session still registered, only stop if it has made no request for
	// a long time — that means the `claude` process died without its
	// SessionEnd hook firing, so the count is stuck. A live but idle tab
	// (open, not being used) keeps the proxy up.
	zombieTimeout = 30 * time.Minute
)

func (l *lifecycle) touch()      { l.mu.Lock(); l.lastSeen = time.Now(); l.mu.Unlock() }
func (l *lifecycle) addSession() { l.mu.Lock(); l.nSess++; l.lastSeen = time.Now(); l.mu.Unlock() }
func (l *lifecycle) endSession() {
	l.mu.Lock()
	if l.nSess > 0 {
		l.nSess--
	}
	l.lastSeen = time.Now()
	l.mu.Unlock()
}
func (l *lifecycle) sessions() int { l.mu.Lock(); defer l.mu.Unlock(); return l.nSess }

func (l *lifecycle) watch(srv *http.Server) {
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	zeroSince := time.Time{}
	for range tick.C {
		l.mu.Lock()
		n, idle := l.nSess, time.Since(l.lastSeen)
		l.mu.Unlock()

		if n == 0 {
			// no claude session — stop shortly after the last request
			if zeroSince.IsZero() {
				zeroSince = time.Now()
			}
			if time.Since(zeroSince) > zeroGrace {
				_ = srv.Close()
				return
			}
		} else {
			zeroSince = time.Time{}
			// a session is registered — keep running even when idle, unless
			// it's been silent long enough to be a crashed (zombie) session
			if idle > zombieTimeout {
				log.Printf("session(s) registered but silent %s — assuming crashed, stopping", idle.Round(time.Minute))
				_ = srv.Close()
				return
			}
		}
	}
}

// rotator holds the in-memory token state for the proxy.
type rotator struct {
	tool string

	mu         sync.Mutex
	order      []string          // profile names, rotation order
	idx        int               // index into order
	tokens     map[string]*token // profile name -> token from its bundle (fallback)
	accounts   map[string]string // profile name -> account email (cached)
	cooldown   map[string]time.Time
	switches   int
	lastSwitch time.Time
}

type token struct {
	Access    string
	Refresh   string
	ExpiresAt time.Time
	account   string
	remaining float64 // last seen unified-remaining fraction/count (-1 unknown)
	resetAt   time.Time
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
	r.accounts = map[string]string{}
	r.cooldown = map[string]time.Time{}
	for _, p := range listProfiles(r.tool) {
		r.order = append(r.order, p.Name)
		r.tokens[p.Name] = loadClaudeToken(r.tool, p.Name)
		r.accounts[p.Name] = p.Account
	}
	if a := readActivePointer(r.tool); a != "" {
		for i, n := range r.order {
			if n == a {
				r.idx = i
			}
		}
	}
}

// refreshFromDisk picks up any profile saved since load() (e.g. a fresh `am
// add`, or a new login just snapshotted by syncActiveFromSystem) without
// disturbing in-memory rotation state (idx, cooldown, switch count) for
// profiles it already knew about.
func (r *rotator) refreshFromDisk() {
	r.mu.Lock()
	defer r.mu.Unlock()
	known := map[string]bool{}
	for _, n := range r.order {
		known[n] = true
	}
	for _, p := range listProfiles(r.tool) {
		if known[p.Name] {
			continue
		}
		r.order = append(r.order, p.Name)
		r.tokens[p.Name] = loadClaudeToken(r.tool, p.Name)
		r.accounts[p.Name] = p.Account
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

// token returns the current access token for the active account.
//
// The active account's credential lives in the system keychain and Claude Code
// is the one that refreshes it (OAuth refresh tokens rotate — only one party
// may hold that job). We read the keychain live so we always forward whatever
// Claude Code most recently refreshed to. The profile bundle is only the
// fallback for an account that isn't the one currently installed.
func (r *rotator) token() string {
	r.mu.Lock()
	name := r.order[r.idx]
	acct := r.metaAccount(name)
	fallback := r.tokens[name]
	r.mu.Unlock()

	if live := liveKeychainToken(); live != nil {
		if acct == "" || live.account == "" || strings.EqualFold(live.account, acct) {
			return live.Access
		}
		// Keychain holds a different account than the profile we think is
		// active — install the active profile so they line up.
		if fallback != nil && fallback.Access != "" {
			installActiveProfile(name)
			return fallback.Access
		}
		return live.Access
	}
	if fallback != nil {
		return fallback.Access
	}
	return ""
}

func (r *rotator) metaAccount(name string) string {
	if r.accounts == nil {
		r.accounts = map[string]string{}
	}
	if a, ok := r.accounts[name]; ok {
		return a
	}
	a := readMeta(r.tool, name).Account
	r.accounts[name] = a
	return a
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
		installActiveProfile(cand) // put cand's creds on the system so Claude Code follows
		log.Printf("ROTATE (%s): %s -> %s", reason, from, cand)
		return
	}
	log.Printf("ROTATE (%s): %s -> (all accounts cooling down; staying)", reason, from)
}

// forceSwitch makes the named profile active immediately (next request uses it).
// Empty name = advance to the next profile in rotation order.
func (r *rotator) forceSwitch(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if name == "" {
		r.idx = (r.idx + 1) % len(r.order)
	} else {
		found := -1
		for i, n := range r.order {
			if n == name {
				found = i
			}
		}
		if found < 0 {
			return fmt.Errorf("no profile %q (have: %s)", name, strings.Join(r.order, ", "))
		}
		r.idx = found
	}
	target := r.order[r.idx]
	delete(r.cooldown, target) // manual switch clears any cooldown on the target
	r.switches++
	r.lastSwitch = time.Now()
	writeActivePointer(r.tool, target)
	installActiveProfile(target)
	log.Printf("MANUAL SWITCH -> %s", target)
	return nil
}

func (r *rotator) status() map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	accts := []map[string]any{}
	for _, n := range r.order {
		t := r.tokens[n]
		m := map[string]any{
			"profile": n,
			"account": r.accounts[n],
			"active":  n == r.order[r.idx],
		}
		if t != nil {
			m["remaining"] = t.remaining
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

// cmdStatus queries a running proxy and prints per-account limit info — the
// same data `/usage` in Claude Code would show for the currently active
// account, since the proxy serves that account's token to every request.
func cmdStatus() {
	if !proxyUp() {
		fmt.Print("proxy not running (it starts automatically when you launch claude)\n\n")
		printLiveLogins()
		return
	}
	resp, err := http.Get(proxyBase() + "/_am/status")
	if err != nil {
		die("status: %v", err)
	}
	defer resp.Body.Close()
	var s struct {
		Tool     string `json:"tool"`
		Switches int    `json:"switches"`
		Accounts []struct {
			Profile    string   `json:"profile"`
			Account    string   `json:"account"`
			Active     bool     `json:"active"`
			Remaining  float64  `json:"remaining"`
			LimitReset string   `json:"limit_reset"`
			Cooldown   string   `json:"cooldown_until"`
		} `json:"accounts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		die("status decode: %v", err)
	}
	fmt.Printf("proxy up · %d switch(es) this run\n\n", s.Switches)
	for _, a := range s.Accounts {
		mark := "  "
		if a.Active {
			mark = "> "
		}
		line := fmt.Sprintf("%s%s", mark, orDash(a.Account))
		if a.Remaining >= 0 {
			line += fmt.Sprintf("   %.0f%% left", a.Remaining*100)
		}
		if a.LimitReset != "" {
			if t, e := time.Parse(time.RFC3339, a.LimitReset); e == nil {
				line += "   resets " + t.Local().Format("15:04")
			}
		}
		if a.Cooldown != "" {
			if t, e := time.Parse(time.RFC3339, a.Cooldown); e == nil {
				line += "   (cooldown until " + t.Local().Format("15:04") + ")"
			}
		}
		fmt.Println(line)
	}
}

// cmdSwitch forces the proxy to the named account right now. The running
// `claude` keeps its session; its next request (and `/usage`) uses the new
// account. Falls back to an on-disk swap if the proxy isn't running.
func cmdSwitch(tool, name string) {
	if tool != "claude" {
		cmdUse(tool, name)
		return
	}
	if !proxyUp() {
		fmt.Println("proxy not running; swapping on-disk credentials instead")
		cmdUse(tool, name)
		return
	}
	resp, err := http.Post(proxyBase()+"/_am/switch?to="+url.QueryEscape(name), "", nil)
	if err != nil {
		die("proxy switch: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		die("proxy switch: %s", strings.TrimSpace(string(b)))
	}
	fmt.Printf("switched to %s (no restart needed)\n", name)
}

func proxyUp() bool {
	c := http.Client{Timeout: 500 * time.Millisecond}
	resp, err := c.Get(proxyBase() + "/_am/status")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return true
}

// proxyEnsureUp starts the background proxy if it isn't already listening and
// registers one Claude session with it. Called by the SessionStart hook.
func proxyEnsureUp() {
	if !proxyUp() {
		spawnProxy()
		for i := 0; i < 50 && !proxyUp(); i++ {
			time.Sleep(100 * time.Millisecond)
		}
		if !proxyUp() {
			fmt.Fprintf(os.Stderr, "am: proxy failed to start (see %s)\n", filepath.Join(baseDir(), "proxy.log"))
			return
		}
	}
	_, _ = http.Post(proxyBase()+"/_am/sync", "", nil)
	_, _ = http.Post(proxyBase()+"/_am/session?op=start", "", nil)
}

// proxyReleaseAndMaybeStop deregisters a session; the proxy stops itself once
// the count reaches zero. Called by the SessionEnd hook.
func proxyReleaseAndMaybeStop() {
	if proxyUp() {
		_, _ = http.Post(proxyBase()+"/_am/session?op=end", "", nil)
	}
}

func spawnProxy() {
	self, err := os.Executable()
	if err != nil {
		die("locate self: %v", err)
	}
	_ = os.MkdirAll(baseDir(), 0o700)
	logf, err := os.OpenFile(filepath.Join(baseDir(), "proxy.log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		die("open proxy.log: %v", err)
	}
	devnull, _ := os.Open(os.DevNull)
	cmd := exec.Command(self, "proxy", "--addr", proxyAddr())
	cmd.Stdin, cmd.Stdout, cmd.Stderr = devnull, logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		die("start proxy: %v", err)
	}
	if devnull != nil {
		_ = devnull.Close()
	}
	_ = cmd.Process.Release()
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

var _ = io.Discard
