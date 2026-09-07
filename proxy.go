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
	// Whatever account Claude is actually logged in as right now takes
	// priority: capture it as a profile if it's new, and make it the active
	// one, so `am claude` always starts on the account you last logged into.
	syncActiveFromSystem("claude")

	profs := listProfiles("claude")
	if len(profs) == 0 {
		die("no claude profiles saved; run `am save claude` (log in first), or `am save claude <name>`")
	}

	rot := &rotator{tool: "claude"}
	rot.load()
	if rot.active() == "" {
		rot.setActive(profs[0].Name)
	}
	fmt.Printf("am proxy: starting on account %q\n", rot.active())

	target, _ := url.Parse(upstream)
	rp := &httputil.ReverseProxy{
		Director: func(r *http.Request) {
			r.URL.Scheme = target.Scheme
			r.URL.Host = target.Host
			r.Host = target.Host
			tok := rot.token()
			hadAuth := r.Header.Get("Authorization") != "" || r.Header.Get("X-Api-Key") != ""
			r.Header.Set("Authorization", "Bearer "+tok)
			r.Header.Del("X-Api-Key")
			if os.Getenv("AM_PROXY_DEBUG") != "" {
				log.Printf("%s %s  (client sent auth: %v -> using %s)", r.Method, r.URL.Path, hadAuth, rot.active())
			}
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
	mux.HandleFunc("/_am/switch", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("to")
		if err := rot.forceSwitch(name); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"active": rot.active()})
	})
	mux.Handle("/", rp)

	fmt.Printf("am proxy on http://%s -> %s\n", addr, upstream)
	fmt.Printf("active claude profile: %s   (rotation order: %s)\n", rot.active(), strings.Join(rot.names(), " -> "))
	fmt.Printf("\npoint Claude Code at it (Claude Code keeps its OAuth identity):\n  export ANTHROPIC_BASE_URL=http://%s\n\n", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
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

// cmdSwitch changes the active account. If the proxy is running it switches the
// proxy live (a running `claude` keeps going, next request uses the new
// account). Otherwise it falls back to a disk swap (am use).
func cmdSwitch(tool, name string) {
	if tool != "claude" {
		cmdUse(tool, name)
		return
	}
	base := "http://" + envOr("AM_PROXY_ADDR", "127.0.0.1:8787")
	if !proxyUp(base) {
		fmt.Println("proxy not running; swapping on-disk credentials instead")
		cmdUse(tool, name)
		return
	}
	resp, err := http.Post(base+"/_am/switch?to="+url.QueryEscape(name), "", nil)
	if err != nil {
		die("proxy switch: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		die("proxy switch: %s", strings.TrimSpace(string(b)))
	}
	fmt.Printf("proxy now serving: %s (running claude keeps its session)\n", name)
}

// cmdRun execs a tool with env pointed at a running proxy.
func cmdRun(tool string, rest []string) { runTool(tool, rest, false) }

// cmdUp is like cmdRun but starts the proxy in the background first if it is
// not already listening.
func cmdUp(tool string, rest []string) { runTool(tool, rest, true) }

func runTool(tool string, rest []string, autostart bool) {
	bin, err := lookPath(tool)
	if err != nil {
		die("%v", err)
	}

	// Only Claude has proxy rotation. Other tools just exec directly — their
	// CLIs re-read their auth file each run, so `am use` already suffices.
	if tool != "claude" {
		execProcess(bin, append([]string{tool}, rest...), os.Environ())
		return
	}

	addr := envOr("AM_PROXY_ADDR", "127.0.0.1:8787")
	base := "http://" + addr
	// Only set ANTHROPIC_BASE_URL, not ANTHROPIC_AUTH_TOKEN: with the token set,
	// Claude Code switches to "API key" mode and stops showing the logged-in
	// account. Without it, Claude Code keeps its OAuth identity (account shows
	// in the UI) and still sends every request through the proxy, which
	// overrides the outbound token for rotation.
	env := append(os.Environ(),
		"ANTHROPIC_BASE_URL="+base,
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
	)
	if os.Getenv("AM_PROXY_AUTH_TOKEN") != "" {
		env = append(env, "ANTHROPIC_AUTH_TOKEN="+os.Getenv("AM_PROXY_AUTH_TOKEN"))
	}

	if !proxyUp(base) {
		if !autostart {
			die("proxy not reachable at %s (start it with: am proxy)", base)
		}
		startProxyBackground(addr)
		for i := 0; i < 50; i++ {
			if proxyUp(base) {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if !proxyUp(base) {
			die("proxy failed to start; see %s", filepath.Join(baseDir(), "proxy.log"))
		}
		fmt.Fprintf(os.Stderr, "am: proxy started in background (log: %s)\n", filepath.Join(baseDir(), "proxy.log"))
	}

	// Claude Code needs a valid OAuth credential on disk to start at all (it
	// checks before making any request). Make sure the active profile's
	// credential is the one installed, so the UI shows the right account.
	ensureActiveCredentialInstalled()

	// Replace this process with the tool — same as running it directly, so its
	// terminal handling (device-attribute queries at startup, mode restore on
	// exit) behaves identically to launching it without `am`.
	execProcess(bin, append([]string{tool}, rest...), env)
}

// ensureActiveCredentialInstalled restores the proxy's active profile onto the
// system if the live login doesn't already match it.
func ensureActiveCredentialInstalled() {
	active := readActivePointer("claude")
	if active == "" {
		return
	}
	live := detectAccount(toolSpec("claude"))
	want := readMeta("claude", active).Account
	if live != "" && live == want {
		return // already correct
	}
	if _, err := os.Stat(bundlePath("claude", active)); err != nil {
		return
	}
	for _, e := range loadProfileEntries("claude", active) {
		_ = applyEntry(e)
	}
}

func proxyUp(base string) bool {
	c := http.Client{Timeout: 500 * time.Millisecond}
	resp, err := c.Get(base + "/_am/status")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return true
}

func startProxyBackground(addr string) {
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
	cmd := exec.Command(self, "proxy", "--addr", addr)
	cmd.Stdin = devnull // never share the tty — nothing the proxy does should
	cmd.Stdout = logf   // ever touch the terminal the user is typing into
	cmd.Stderr = logf
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

func fmtFrac(f float64) string {
	if f < 0 {
		return "?"
	}
	return fmt.Sprintf("%.0f%%", f*100)
}

var _ = io.Discard
