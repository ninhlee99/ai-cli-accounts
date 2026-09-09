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
// `am proxy up` is what the Claude Code SessionStart hook calls to start it
// on demand; `am proxy down` (SessionEnd) just deregisters the session for
// `am status`'s count — it no longer stops the server, so
// ANTHROPIC_BASE_URL=127.0.0.1:8787 is never left pointing at a dead
// listener mid-session. `am proxy down --force` actually stops it, but
// refuses while any claude tab is still attached (add --yes-i-know to
// override) — see proxyForceStop.
func cmdProxy(args []string) {
	if len(args) > 0 {
		switch args[0] {
		case "up":
			proxyEnsureUp()
			return
		case "down":
			if len(args) > 1 && args[1] == "--force" {
				proxyForceStop(len(args) > 2 && args[2] == "--yes-i-know")
			} else {
				proxyReleaseAndMaybeStop()
			}
			return
		}
	}
	runProxyForeground(args)
}

// runProxyForeground is the actual server. It runs indefinitely — stop it
// with `am proxy down --force` — so it never disappears out from under a
// claude session that's still pointed at it.
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
	go rot.periodicSnapshot()

	life := &lifecycle{}

	target, _ := url.Parse(upstream)
	rp := &httputil.ReverseProxy{
		Director: func(r *http.Request) {
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
		ModifyResponse: func(resp *http.Response) error {
			rot.observe(resp)
			wrapUsageCapture(resp, rot.active())
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
		s := rot.status()
		s["sessions"] = life.sessions()
		s["upstream"] = upstream
		_ = json.NewEncoder(w).Encode(s)
	})
	mux.HandleFunc("/_am/switch", func(w http.ResponseWriter, r *http.Request) {
		if err := rot.forceSwitch(r.URL.Query().Get("to")); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"active": rot.active()})
	})
	// Hooks register/deregister a session by the claude process's PID —
	// tracked only so `am status` can show how many tabs are attached.
	mux.HandleFunc("/_am/session", func(w http.ResponseWriter, r *http.Request) {
		pid, _ := strconv.Atoi(r.URL.Query().Get("pid"))
		if pid > 0 {
			switch r.URL.Query().Get("op") {
			case "start":
				life.addSession(pid)
			case "end":
				life.endSession(pid)
			}
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
	mux.HandleFunc("/_am/shutdown", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
		go func() { time.Sleep(100 * time.Millisecond); _ = srv.Close() }()
	})

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

// lifecycle tracks active Claude sessions by PID for `am status` to report
// ("N claude tab(s) attached"). It no longer decides whether the proxy stays
// up — the proxy now runs indefinitely once started (see runProxyForeground)
// so ANTHROPIC_BASE_URL pointing at 127.0.0.1:8787 is never left dangling
// with nothing listening: it was mid-session auto-shutdown that caused that,
// not a one-time startup failure. Stop it explicitly with `am proxy down --force`.
type lifecycle struct {
	mu   sync.Mutex
	pids map[int]bool
}

func (l *lifecycle) addSession(pid int) {
	l.mu.Lock()
	if l.pids == nil {
		l.pids = map[int]bool{}
	}
	l.pids[pid] = true
	l.mu.Unlock()
}
func (l *lifecycle) endSession(pid int) {
	l.mu.Lock()
	delete(l.pids, pid)
	l.mu.Unlock()
}
func (l *lifecycle) sessions() int { return l.pruneDead() }

// pruneDead drops any tracked PID that's no longer alive (process crashed
// without its SessionEnd hook firing) and returns how many remain. Only
// affects the count `am status` shows — never shuts the proxy down.
func (l *lifecycle) pruneDead() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	for pid := range l.pids {
		if err := syscall.Kill(pid, 0); err != nil {
			delete(l.pids, pid)
		}
	}
	return len(l.pids)
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
	dead       map[string]bool // profile name -> refresh token confirmed dead; skip in rotate() until re-login
	switches   int
	lastSwitch time.Time

	// Per-account switch tallies, split by who initiated it — `am status`
	// shows these so "why did it leave this account" is answerable without
	// grepping proxy.log. Keyed by the account switched AWAY FROM (the one
	// that stopped being usable at that moment).
	autoSwitches   map[string]int // rotate(): 429 or near-limit
	manualSwitches map[string]int // forceSwitch(): `am switch` / hook-driven
}

type token struct {
	Access    string
	Refresh   string
	ExpiresAt time.Time
	account   string
	remaining float64 // last seen unified-remaining fraction/count (-1 unknown) — drives rotation
	resetAt   time.Time

	// Separate 5h/7d windows, straight from anthropic-ratelimit-unified-{5h,7d}-*
	// headers (Claude Code's own /usage draws from the same two). -1 = unknown.
	fiveH  window
	sevenD window
}

type window struct {
	used    float64 // utilization, 0..1
	resetAt time.Time
	known   bool
}

const (
	// rotate when the account reports this fraction (or fewer) of its window left
	rotateThreshold = 0.06
	// don't return to an account that hit a limit until this long after its reset
	cooldownPad = 30 * time.Second
	// refreshLead (used for token-expiry checks) is defined in claude_token.go
)

func (r *rotator) load() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tokens = map[string]*token{}
	r.accounts = map[string]string{}
	r.cooldown = map[string]time.Time{}
	r.dead = map[string]bool{}
	r.autoSwitches = map[string]int{}
	r.manualSwitches = map[string]int{}
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
// profiles it already knew about. It also clears any dead-refresh
// blacklist entry for a profile whose bundle changed since we last cached
// it — that's exactly what a re-login/re-save does, and it's the signal
// that the account is trustworthy again.
func (r *rotator) refreshFromDisk() {
	r.mu.Lock()
	defer r.mu.Unlock()
	known := map[string]bool{}
	for _, n := range r.order {
		known[n] = true
	}
	for _, p := range listProfiles(r.tool) {
		if !known[p.Name] {
			r.order = append(r.order, p.Name)
			r.tokens[p.Name] = loadClaudeToken(r.tool, p.Name)
			r.accounts[p.Name] = p.Account
			continue
		}
		if r.dead[p.Name] {
			fresh := loadClaudeToken(r.tool, p.Name)
			old := r.tokens[p.Name]
			if fresh != nil && (old == nil || old.Access != fresh.Access || old.Refresh != fresh.Refresh) {
				r.tokens[p.Name] = fresh
				delete(r.dead, p.Name)
				log.Printf("am: %s re-logged in — cleared dead-refresh flag", p.Name)
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
		// active — install the active profile so they line up. This also
		// refreshes the profile's token if it was expired/rotated-out, so
		// re-read the keychain afterward rather than trusting the (possibly
		// stale) bundle token we cached at load().
		if fallback != nil && fallback.Access != "" {
			installActiveProfile(name)
			if refreshed := liveKeychainToken(); refreshed != nil {
				return refreshed.Access
			}
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
//
// Anthropic reports two independent windows per account — 5h and 7d — via
// anthropic-ratelimit-unified-{5h,7d}-{utilization,reset}. This is the same
// data Claude Code's own `/usage` reads, so `am status` mirrors it exactly
// instead of re-deriving a fraction from remaining/limit counts (which some
// window variants don't send). Rotation still keys off the 5h window: it's
// the one that actually blocks the very next request.
func (r *rotator) observe(resp *http.Response) {
	r.mu.Lock()
	name := r.order[r.idx]
	t := r.tokens[name]
	r.mu.Unlock()
	if t == nil {
		return
	}

	h := resp.Header
	fiveH := parseWindow(h, "5h")
	sevenD := parseWindow(h, "7d")

	// Fallback for older/plain responses that only send the un-suffixed
	// unified-remaining/-limit pair (no per-window utilization).
	rem, remOK := parseFirstFloat(h, "anthropic-ratelimit-unified-remaining", "anthropic-ratelimit-requests-remaining")
	lim, limOK := parseFirstFloat(h, "anthropic-ratelimit-unified-limit", "anthropic-ratelimit-requests-limit")
	frac := -1.0
	if remOK && limOK && lim > 0 {
		frac = 1 - rem/lim
	} else if remOK {
		frac = -1 // count-only, no window total to compare against
	}

	r.mu.Lock()
	if fiveH.known {
		t.fiveH = fiveH
	}
	if sevenD.known {
		t.sevenD = sevenD
	}
	switch {
	case fiveH.known:
		t.remaining = 1 - fiveH.used
	case frac >= 0:
		t.remaining = 1 - frac
	case remOK:
		t.remaining = rem // count-only; treat small absolute as low
	}
	if !t.fiveH.resetAt.IsZero() {
		t.resetAt = t.fiveH.resetAt
	}
	r.mu.Unlock()

	hardLimited := resp.StatusCode == http.StatusTooManyRequests
	nearLimit := (fiveH.known && fiveH.used >= 1-rotateThreshold) ||
		(!fiveH.known && remOK && !limOK && rem <= 2)

	if hardLimited || nearLimit {
		reason := "near limit"
		if hardLimited {
			reason = "429"
		}
		r.rotate(name, reason)
	}
}

// parseWindow reads anthropic-ratelimit-unified-<suffix>-{utilization,reset}
// for one window ("5h" or "7d"). known=false when the header wasn't sent
// (older API responses, or the un-suffixed fallback headers only).
func parseWindow(h http.Header, suffix string) window {
	u := strings.TrimSpace(h.Get("anthropic-ratelimit-unified-" + suffix + "-utilization"))
	if u == "" {
		return window{}
	}
	used, err := strconv.ParseFloat(u, 64)
	if err != nil {
		return window{}
	}
	reset := parseFirstTime(h, "anthropic-ratelimit-unified-"+suffix+"-reset")
	return window{used: used, resetAt: reset, known: true}
}

// periodicSnapshot keeps the active profile's bundle in sync with whatever
// Claude Code has rotated into the live keychain. Refresh tokens are
// single-use / rotate-on-use, so the only way to keep a backgrounded
// account's bundle usable is to capture the rotation the moment it happens,
// while that account is still active — waiting until it's switched back in
// is too late, the old refresh token is already burned by then.
func (r *rotator) periodicSnapshot() {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for range t.C {
		r.snapshotActiveIfChanged()
	}
}

func (r *rotator) snapshotActiveIfChanged() {
	r.mu.Lock()
	name := r.order[r.idx]
	r.mu.Unlock()
	live := liveKeychainToken()
	if live == nil {
		return
	}
	saved := loadClaudeToken(r.tool, name)
	if saved != nil && saved.Access == live.Access && saved.Refresh == live.Refresh {
		return // nothing changed, don't touch disk
	}
	cmdSave(r.tool, name)
	r.mu.Lock()
	r.tokens[name] = live
	r.mu.Unlock()
	log.Printf("am: re-synced %s bundle (token rotated while active)", name)
}

func (r *rotator) rotate(from, reason string) {
	// Capture whatever Claude Code last rotated into the keychain for `from`
	// before we overwrite it with the next account's creds — otherwise a
	// refresh token rotation that happened while `from` was active is lost.
	r.snapshotActiveIfChanged()

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
		if r.dead[cand] {
			continue // refresh token confirmed dead; skip until re-login clears it
		}
		if !installActiveProfile(cand) { // put cand's creds on the system so Claude Code follows
			r.dead[cand] = true
			log.Printf("ROTATE (%s): %s -> %s refresh dead, blacklisting until re-login", reason, from, cand)
			continue
		}
		r.idx = (r.idx + step) % n
		r.switches++
		r.autoSwitches[from]++
		r.lastSwitch = time.Now()
		writeActivePointer(r.tool, cand)
		log.Printf("ROTATE (%s): %s -> %s", reason, from, cand)
		return
	}
	log.Printf("ROTATE (%s): %s -> (all accounts cooling down or dead; staying)", reason, from)
}

// forceSwitch makes the named profile active immediately (next request uses it).
// Empty name = advance to the next profile in rotation order.
func (r *rotator) forceSwitch(name string) error {
	r.snapshotActiveIfChanged()

	r.mu.Lock()
	defer r.mu.Unlock()
	from := r.order[r.idx]
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
	delete(r.dead, target)     // ...and any dead-refresh blacklist; user is vouching for it
	r.switches++
	if target != from {
		r.manualSwitches[from]++
	}
	r.lastSwitch = time.Now()
	writeActivePointer(r.tool, target)
	if !installActiveProfile(target) {
		r.dead[target] = true
		return fmt.Errorf("refresh token for %q is dead — log into it again before switching to it", target)
	}
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
			"profile":         n,
			"account":         r.accounts[n],
			"active":          n == r.order[r.idx],
			"auto_switches":   r.autoSwitches[n],
			"manual_switches": r.manualSwitches[n],
		}
		if t != nil {
			m["remaining"] = t.remaining
			if !t.resetAt.IsZero() {
				m["limit_reset"] = t.resetAt
			}
			if t.fiveH.known {
				m["5h_used"] = t.fiveH.used
				if !t.fiveH.resetAt.IsZero() {
					m["5h_reset"] = t.fiveH.resetAt
				}
			}
			if t.sevenD.known {
				m["7d_used"] = t.sevenD.used
				if !t.sevenD.resetAt.IsZero() {
					m["7d_reset"] = t.sevenD.resetAt
				}
			}
		}
		if cd, ok := r.cooldown[n]; ok && time.Now().Before(cd) {
			m["cooldown_until"] = cd
		}
		if r.dead[n] {
			m["dead"] = true
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

const defaultUpstream = "https://api.anthropic.com"

// cmdStatus queries a running proxy and prints per-account limit info — the
// same data `/usage` in Claude Code would show for the currently active
// account, since the proxy serves that account's token to every request.
// dotGreen/dotGray color the status dot so "running" reads at a glance
// instead of blending into the rest of the line — grey (not red) for "not
// running" since that's the normal idle state, not an error.
const (
	dotGreen = "\x1b[32m●\x1b[0m"
	dotGray  = "\x1b[90m○\x1b[0m"
)

func cmdStatus() {
	if !proxyUp() {
		fmt.Printf("proxy:     %s not running (starts automatically when you launch claude, stays up until `am proxy down --force`)\n", dotGray)
		fmt.Printf("base URL:  %s  (direct — no rotation, no auto-switch on rate limit)\n\n", defaultUpstream)
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
		Sessions int    `json:"sessions"`
		Upstream string `json:"upstream"`
		Accounts []struct {
			Profile        string   `json:"profile"`
			Account        string   `json:"account"`
			Active         bool     `json:"active"`
			Remaining      float64  `json:"remaining"`
			LimitReset     string   `json:"limit_reset"`
			Cooldown       string   `json:"cooldown_until"`
			Dead           bool     `json:"dead"`
			FiveHUsed      *float64 `json:"5h_used"`
			FiveHReset     string   `json:"5h_reset"`
			SevenDUsed     *float64 `json:"7d_used"`
			SevenDReset    string   `json:"7d_reset"`
			AutoSwitches   int      `json:"auto_switches"`
			ManualSwitches int      `json:"manual_switches"`
		} `json:"accounts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		die("status decode: %v", err)
	}
	fmt.Printf("proxy:     %s running on %s · %d claude tab(s) attached · %d switch(es) this run\n", dotGreen, proxyAddr(), s.Sessions, s.Switches)
	fmt.Printf("base URL:  %s  (rotating through %d account(s) below)\n\n", proxyBase(), len(s.Accounts))
	for _, a := range s.Accounts {
		mark := "  "
		state := "idle"
		if a.Active {
			mark = "> "
			state = "active"
		}
		if a.Dead {
			state = "dead"
		} else if a.Cooldown != "" {
			if t, e := time.Parse(time.RFC3339, a.Cooldown); e == nil && time.Now().Before(t) {
				state = "cooldown"
			}
		}
		line := fmt.Sprintf("%s%-8s  %s", mark, state, orDash(a.Account))
		if a.Cooldown != "" {
			if t, e := time.Parse(time.RFC3339, a.Cooldown); e == nil {
				line += "   (cooldown until " + t.Local().Format("15:04") + ")"
			}
		}
		if a.Dead {
			line += "   (refresh dead — re-login to restore)"
		}
		fmt.Println(line)
		if a.FiveHUsed != nil {
			fmt.Printf("           5h limit:  %.0f%% used%s\n", *a.FiveHUsed*100, resetSuffix(a.FiveHReset))
		}
		if a.SevenDUsed != nil {
			fmt.Printf("           7d limit:  %.0f%% used%s\n", *a.SevenDUsed*100, resetSuffix(a.SevenDReset))
		}
		if a.AutoSwitches > 0 || a.ManualSwitches > 0 {
			fmt.Printf("           switched away: %d auto (429/near-limit), %d manual\n", a.AutoSwitches, a.ManualSwitches)
		}
		if a.FiveHUsed == nil && a.SevenDUsed == nil && a.Remaining >= 0 {
			// Older/plain response only — no per-window breakdown available yet.
			line := fmt.Sprintf("           %.0f%% left", a.Remaining*100)
			if a.LimitReset != "" {
				if t, e := time.Parse(time.RFC3339, a.LimitReset); e == nil {
					line += "   resets " + t.Local().Format("15:04")
				}
			}
			fmt.Println(line)
		}
	}
}

func resetSuffix(iso string) string {
	if iso == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		return ""
	}
	return "   resets " + t.Local().Format("Mon 15:04")
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
	// The hook process is a direct child of `claude`, so its parent PID is
	// the claude process this session belongs to — that's what the proxy
	// tracks liveness by.
	_, _ = http.Post(proxyBase()+"/_am/session?op=start&pid="+strconv.Itoa(os.Getppid()), "", nil)
}

// proxyReleaseAndMaybeStop deregisters a session for `am status`'s count.
// Called by the SessionEnd hook. The name is legacy — it no longer stops
// anything; the proxy stays up so a still-live tab is never left pointed at
// a dead port.
func proxyReleaseAndMaybeStop() {
	if proxyUp() {
		_, _ = http.Post(proxyBase()+"/_am/session?op=end&pid="+strconv.Itoa(os.Getppid()), "", nil)
	}
}

// proxyForceStop actually shuts the background proxy down. `am status`
// afterwards will show the base URL falling back to the real Anthropic API.
//
// Any claude tab already running has ANTHROPIC_BASE_URL=127.0.0.1:8787 baked
// into its process environment — read once at its own startup, not am's to
// change. Stopping the proxy out from under one leaves it pointed at a dead
// port for the rest of its life (connection refused, no auto-fallback). So
// this refuses when sessions are attached unless overridden with
// --yes-i-know, same idea as `rm -rf` wanting `-f` to skip the safety check.
func proxyForceStop(skipCheck bool) {
	if !proxyUp() {
		fmt.Println("proxy not running")
		return
	}
	if !skipCheck {
		if n := attachedSessions(); n != 0 {
			if n < 0 {
				die("couldn't confirm 0 claude tab(s) attached (status check failed) — "+
					"stopping now risks leaving one pointed at a dead port. "+
					"`am proxy down --force --yes-i-know` to stop anyway.")
			}
			die("%d claude tab(s) still attached — stopping now leaves them pointed at a dead port "+
				"(ANTHROPIC_BASE_URL is fixed for the life of that process). "+
				"Close those tabs first, or `am proxy down --force --yes-i-know` to stop anyway.", n)
		}
	}
	resp, err := http.Post(proxyBase()+"/_am/shutdown", "", nil)
	if err != nil {
		die("shutdown request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		die("shutdown request returned %s (binary running may be stale — rebuild/reinstall am)", resp.Status)
	}
	fmt.Println("proxy stopped")
}

// attachedSessions returns the claude-tab count from the running proxy, or
// -1 if that can't be confirmed (request/decode failure) — the caller must
// treat -1 as "unknown, assume attached" rather than as zero, or a stale/
// misbehaving proxy would silently bypass the force-stop safety check.
func attachedSessions() int {
	resp, err := http.Get(proxyBase() + "/_am/status")
	if err != nil {
		return -1
	}
	defer resp.Body.Close()
	var s struct {
		Sessions int `json:"sessions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return -1
	}
	return s.Sessions
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
