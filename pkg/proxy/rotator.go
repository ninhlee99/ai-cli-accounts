package proxy

import (
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"amux-accounts/pkg/auth"
	"amux-accounts/pkg/profile"
	"amux-accounts/pkg/term"
	"amux-accounts/pkg/types"
)

const (
	// DefaultUsedThreshold is the utilization (0–1) at which Observe
	// auto-rotates. CLI exposes this as --threshold percent (default 95).
	DefaultUsedThreshold = 0.95
	// don't return to an account that hit a limit until this long after its reset
	cooldownPad = 30 * time.Second
	refreshLead = 2 * time.Minute
)

// package-level default applied by NewRotator; set via SetUsedThreshold
// before RunProxy / RunSupervisor so --threshold / AM_ROTATE_THRESHOLD
// reach the daemon without threading a new arg through every call site.
var usedThresholdDefault = DefaultUsedThreshold

// SetUsedThreshold records the auto-rotate utilization threshold for
// subsequent NewRotator calls. Accepts either a percent (95) or a
// fraction (0.95); invalid / zero falls back to DefaultUsedThreshold.
func SetUsedThreshold(v float64) {
	usedThresholdDefault = ParseUsedThreshold(v)
}

// UsedThreshold returns the currently configured package default.
func UsedThreshold() float64 { return usedThresholdDefault }

// ParseUsedThreshold normalizes a CLI / env value to a 0–1 fraction.
// Values > 1 are treated as percents (95 → 0.95). ≤0 or >100 → default.
func ParseUsedThreshold(v float64) float64 {
	if v <= 0 {
		return DefaultUsedThreshold
	}
	if v > 1 {
		if v > 100 {
			return DefaultUsedThreshold
		}
		return v / 100
	}
	return v
}

// Rotator holds the in-memory token state and auto-rotates on rate limits.
type Rotator struct {
	tool string

	mu       sync.Mutex
	order    []string // profile names, rotation order
	idx      int      // index into order
	tokens   map[string]*types.Token
	accounts map[string]string // profile name -> account email (cached)
	cooldown map[string]time.Time
	dead     map[string]bool // profile name -> refresh token confirmed dead; skip in Rotate() until re-login
	disabled map[string]bool // profile name -> am off; skip rotate + reject am sw until am on

	// usedThreshold: rotate when window utilization >= this (default 0.95).
	usedThreshold float64

	switches   int
	lastSwitch time.Time

	// Per-account switch tallies, split by who initiated it — `am status`
	// shows these so "why did it leave this account" is answerable without
	// grepping proxy.log. Keyed by the account switched AWAY FROM (the one
	// that stopped being usable at that moment).
	autoSwitches   map[string]int // Rotate(): 429 or near-limit
	manualSwitches map[string]int // ForceSwitch(): `am switch` / hook-driven
}

func NewRotator(tool string) *Rotator {
	r := &Rotator{tool: tool, usedThreshold: usedThresholdDefault}
	r.Load()
	return r
}

func (r *Rotator) Load() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tokens = map[string]*types.Token{}
	r.accounts = map[string]string{}
	r.cooldown = map[string]time.Time{}
	r.dead = map[string]bool{}
	r.disabled = map[string]bool{}
	r.autoSwitches = map[string]int{}
	r.manualSwitches = map[string]int{}
	r.order = nil
	for _, p := range profile.ListProfiles(r.tool) {
		r.order = append(r.order, p.Name)
		r.tokens[p.Name] = profile.LoadClaudeToken(r.tool, p.Name)
		r.accounts[p.Name] = p.Account
		r.disabled[p.Name] = p.Disabled
	}
	if a := profile.ReadActivePointer(r.tool); a != "" {
		for i, n := range r.order {
			if n == a {
				r.idx = i
			}
		}
	}
}

// RefreshFromDisk picks up any profile saved since Load() (a fresh `am add`,
// or a new login) without disturbing in-memory rotation state (idx,
// cooldown, switch counts) for profiles it already knew about. It also
// clears any dead-refresh blacklist entry for a profile whose bundle
// changed since it was last cached — that's exactly what a re-login/re-save
// does, and it's the signal that the account is trustworthy again.
func (r *Rotator) RefreshFromDisk() {
	r.mu.Lock()
	defer r.mu.Unlock()
	known := map[string]bool{}
	for _, n := range r.order {
		known[n] = true
	}
	for _, p := range profile.ListProfiles(r.tool) {
		if r.disabled == nil {
			r.disabled = map[string]bool{}
		}
		r.disabled[p.Name] = p.Disabled
		if !known[p.Name] {
			r.order = append(r.order, p.Name)
			r.tokens[p.Name] = profile.LoadClaudeToken(r.tool, p.Name)
			r.accounts[p.Name] = p.Account
			continue
		}
		r.accounts[p.Name] = p.Account
		if r.dead[p.Name] {
			fresh := profile.LoadClaudeToken(r.tool, p.Name)
			old := r.tokens[p.Name]
			if fresh != nil && (old == nil || old.Access != fresh.Access || old.Refresh != fresh.Refresh) {
				r.tokens[p.Name] = fresh
				delete(r.dead, p.Name)
				log.Printf("amux: %s re-logged in — cleared dead-refresh flag", p.Name)
			}
		}
	}
}

func (r *Rotator) Names() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.order))
	copy(out, r.order)
	return out
}

func (r *Rotator) Active() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.order) == 0 {
		return ""
	}
	return r.order[r.idx]
}

func (r *Rotator) SetActive(name string) {
	r.mu.Lock()
	for i, n := range r.order {
		if n == name {
			r.idx = i
		}
	}
	r.mu.Unlock()
	profile.WriteActivePointer(r.tool, name)
}

// Token returns the current access token for the active account.
//
// The active account's credential lives in the system keychain and Claude
// Code is the one that refreshes it (OAuth refresh tokens rotate — only one
// party may hold that job). We read the keychain live so we always forward
// whatever Claude Code most recently refreshed to. The profile bundle is
// only the fallback for an account that isn't the one currently installed.
func (r *Rotator) Token() string {
	r.mu.Lock()
	if len(r.order) == 0 || r.idx >= len(r.order) {
		r.mu.Unlock()
		return ""
	}
	name := r.order[r.idx]
	if r.dead[name] || r.disabled[name] {
		r.mu.Unlock()
		return ""
	}
	acct := r.accounts[name]
	fallback := r.tokens[name]
	r.mu.Unlock()

	live := auth.LiveKeychainToken()
	if live == nil {
		if fallback != nil && !auth.TokenExpiryNeedsRefresh(fallback.ExpiresAt.UnixMilli()) {
			return fallback.Access
		}
		return ""
	}
	// auth.ParseClaudeCreds can't know which account is logged in (that
	// requires reading ~/.claude.json, which lives in pkg/profile and would
	// be an import cycle) — so fill it in here from the live keychain state.
	live.Account = profile.DetectAccount(profile.ToolSpec(r.tool))

	if acct == "" || live.Account == "" || strings.EqualFold(live.Account, acct) {
		if !auth.TokenExpiryNeedsRefresh(live.ExpiresAt.UnixMilli()) {
			return live.Access
		}
		// Active profile is expired; attempt to refresh the live keychain token.
		if newAccess, err := auth.RefreshLiveClaudeToken(); err == nil {
			profile.SaveActiveProfile(r.tool, name)
			return newAccess
		}
		r.mu.Lock()
		r.dead[name] = true
		r.mu.Unlock()
		term.LogAuth("%s expired & refresh failed — blacklist", name)
		return ""
	}

	// Keychain holds a different account than the profile we think is
	// active — install the active profile so they line up. This also
	// refreshes the profile's token if it was expired/rotated-out, so
	// re-read the keychain afterward rather than trusting the (possibly
	// stale) bundle token cached at Load().
	if fallback != nil && fallback.Access != "" {
		if !profile.InstallActiveProfile(name) {
			r.mu.Lock()
			r.dead[name] = true
			r.mu.Unlock()
			return ""
		}
		if refreshed := auth.LiveKeychainToken(); refreshed != nil && !auth.TokenExpiryNeedsRefresh(refreshed.ExpiresAt.UnixMilli()) {
			return refreshed.Access
		}
		if !auth.TokenExpiryNeedsRefresh(fallback.ExpiresAt.UnixMilli()) {
			return fallback.Access
		}
		return ""
	}
	if !auth.TokenExpiryNeedsRefresh(live.ExpiresAt.UnixMilli()) {
		return live.Access
	}
	return ""
}

// Observe reads rate-limit headers off each response and rotates if needed.
// A 401 marks the active account's refresh dead and rotates immediately —
// that's Claude Code (or Anthropic) telling us this access+refresh pair no
// longer works, independent of any rate-limit signal.
func (r *Rotator) Observe(resp *http.Response) {
	r.mu.Lock()
	if len(r.order) == 0 || r.idx >= len(r.order) {
		r.mu.Unlock()
		return
	}
	name := r.order[r.idx]
	t := r.tokens[name]
	r.mu.Unlock()

	if resp.StatusCode == http.StatusUnauthorized {
		r.mu.Lock()
		r.dead[name] = true
		r.mu.Unlock()
		term.LogAuth("401 for %s — marking dead", name)
		r.Rotate(name, "401 unauthorized")
		return
	}
	if t == nil {
		return
	}

	h := resp.Header
	fiveH := parseWindow(h, "5h")
	sevenD := parseWindow(h, "7d")

	// Fallback for older/plain responses that only send the un-suffixed
	// unified-remaining/-limit pair (no per-window utilization).
	rem, remOK := parseFirstFloat(h,
		"anthropic-ratelimit-unified-remaining",
		"anthropic-ratelimit-requests-remaining",
	)
	lim, limOK := parseFirstFloat(h,
		"anthropic-ratelimit-unified-limit",
		"anthropic-ratelimit-requests-limit",
	)
	frac := -1.0
	if remOK && limOK && lim > 0 {
		frac = 1 - rem/lim
	}

	r.mu.Lock()
	if fiveH.Known {
		t.FiveH = fiveH
	}
	if sevenD.Known {
		t.SevenD = sevenD
	}
	switch {
	case fiveH.Known:
		t.Remaining = 1 - fiveH.Used
	case frac >= 0:
		t.Remaining = 1 - frac
	case remOK:
		t.Remaining = rem // count-only; treat small absolute as low
	}
	if !t.FiveH.ResetAt.IsZero() {
		t.ResetAt = t.FiveH.ResetAt
	}
	r.mu.Unlock()

	r.mu.Lock()
	thresh := r.usedThreshold
	if thresh <= 0 {
		thresh = DefaultUsedThreshold
	}
	r.mu.Unlock()

	hardLimited := resp.StatusCode == http.StatusTooManyRequests
	nearLimit := (fiveH.Known && fiveH.Used >= thresh) ||
		(!fiveH.Known && remOK && !limOK && rem <= 2) ||
		(!fiveH.Known && frac >= thresh)

	if hardLimited || nearLimit {
		reason := "near limit"
		if hardLimited {
			reason = "429"
		}
		r.Rotate(name, reason)
	}
}

// AllUnavailable reports whether every saved profile is either in cooldown,
// marked dead, or turned off — i.e. Claude reverse-proxy has nowhere useful
// to go and the gateway should fall over to the free provider pool.
func (r *Rotator) AllUnavailable() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.order) == 0 {
		return true
	}
	now := time.Now()
	for _, n := range r.order {
		if r.disabled[n] || r.dead[n] {
			continue
		}
		if cd, ok := r.cooldown[n]; ok && now.Before(cd) {
			continue
		}
		return false
	}
	return true
}

// ProfileCount returns how many Claude Code profiles are usable — i.e. not
// turned off (`am off`). A disabled profile must never be treated as an
// available option by callers deciding whether Claude is "usable" (see
// server.go's claudeUsable / usePool logic): off means off, even when it's
// the only Claude profile the user has.
func (r *Rotator) ProfileCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, name := range r.order {
		if !r.disabled[name] {
			n++
		}
	}
	return n
}

// TotalProfileCount returns every Claude Code profile the rotator knows
// about, including disabled ones — for display/status purposes only.
func (r *Rotator) TotalProfileCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.order)
}

// ShouldFailoverToProviderPool is true when every Claude Code profile is
// cooling or dead. Callers then bridge to the tool failover pool (API→web,
// As soon as any Claude cooldown expires, this returns
// false and the next request goes back through Anthropic.
func (r *Rotator) ShouldFailoverToProviderPool() bool {
	return r.ProfileCount() > 0 && r.AllUnavailable()
}

// EnsureUsableActive makes sure the active Claude profile is one that is
// not cooling/dead. Used when returning from provider-pool failover after a
// rate-limit window resets — picks the first available account and installs
// its credentials. Returns false only when every profile is still unusable.
func (r *Rotator) EnsureUsableActive() bool {
	r.mu.Lock()
	if len(r.order) == 0 {
		r.mu.Unlock()
		return false
	}
	now := time.Now()
	usable := func(n string) bool {
		if r.disabled[n] || r.dead[n] {
			return false
		}
		if cd, ok := r.cooldown[n]; ok && now.Before(cd) {
			return false
		}
		return true
	}
	cur := r.order[r.idx]
	if usable(cur) {
		r.mu.Unlock()
		return true
	}
	from := cur
	for i, n := range r.order {
		if !usable(n) {
			continue
		}
		r.idx = i
		r.switches++
		r.autoSwitches[from]++
		r.lastSwitch = now
		target := n
		r.mu.Unlock()

		profile.WriteActivePointer(r.tool, target)
		if !profile.InstallActiveProfile(target) {
			r.mu.Lock()
			r.dead[target] = true
			// keep searching under lock — fall through by re-entering loop
			// via recursive-style continue: re-lock and try next.
			// Simpler: mark dead and call EnsureUsableActive again.
			r.mu.Unlock()
			log.Printf("amux: Claude %s refresh dead while recovering, trying next", target)
			return r.EnsureUsableActive()
		}
		term.LogOK("Claude available — switched back to %s", target)
		return true
	}
	r.mu.Unlock()
	return false
}

// parseWindow reads anthropic-ratelimit-unified-<suffix>-{utilization,reset}
// for one window ("5h" or "7d"). Known=false when the header wasn't sent.
func parseWindow(h http.Header, suffix string) types.Window {
	u := strings.TrimSpace(h.Get("anthropic-ratelimit-unified-" + suffix + "-utilization"))
	if u == "" {
		return types.Window{}
	}
	used, err := strconv.ParseFloat(u, 64)
	if err != nil {
		return types.Window{}
	}
	reset := parseFirstTime(h, "anthropic-ratelimit-unified-"+suffix+"-reset")
	return types.Window{Used: used, ResetAt: reset, Known: true}
}

// PeriodicSnapshot keeps the active profile's bundle in sync with whatever
// Claude Code has rotated into the live keychain. Refresh tokens are
// single-use/rotate-on-use, so the only way to keep a backgrounded
// account's bundle usable is to capture the rotation the moment it happens,
// while that account is still active — waiting until it's switched back in
// is too late, the old refresh token is already burned by then. Call this
// once in a goroutine after the proxy starts.
func (r *Rotator) PeriodicSnapshot() {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for range t.C {
		r.snapshotActiveIfChanged()
	}
}

func (r *Rotator) snapshotActiveIfChanged() {
	r.mu.Lock()
	if len(r.order) == 0 || r.idx >= len(r.order) {
		r.mu.Unlock()
		return
	}
	name := r.order[r.idx]
	r.mu.Unlock()

	live := auth.LiveKeychainToken()
	if live == nil {
		return
	}
	saved := profile.LoadClaudeToken(r.tool, name)
	if saved != nil && saved.Access == live.Access && saved.Refresh == live.Refresh {
		return // nothing changed, don't touch disk
	}
	profile.SaveActiveProfile(r.tool, name)
	r.mu.Lock()
	r.tokens[name] = live
	r.mu.Unlock()
	log.Printf("amux: re-synced %s bundle (token rotated while active)", name)
}

func (r *Rotator) Rotate(from, reason string) {
	// Capture whatever Claude Code last rotated into the keychain for `from`
	// before overwriting it with the next account's creds — otherwise a
	// refresh-token rotation that happened while `from` was active is lost.
	r.snapshotActiveIfChanged()

	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.order) == 0 || r.order[r.idx] != from {
		return // already moved on
	}
	if t := r.tokens[from]; t != nil && !t.ResetAt.IsZero() {
		r.cooldown[from] = t.ResetAt.Add(cooldownPad)
	} else {
		r.cooldown[from] = time.Now().Add(15 * time.Minute)
	}
	n := len(r.order)
	for step := 1; step <= n; step++ {
		cand := r.order[(r.idx+step)%n]
		if r.disabled[cand] {
			continue // am off — skip until am on
		}
		if cd, ok := r.cooldown[cand]; ok && time.Now().Before(cd) {
			continue
		}
		if r.dead[cand] {
			continue // refresh token confirmed dead; skip until re-login clears it
		}
		if !profile.InstallActiveProfile(cand) {
			r.dead[cand] = true
			term.LogRotate("%s: %s → %s (refresh dead — blacklist)", reason, from, cand)
			continue
		}
		r.idx = (r.idx + step) % n
		r.switches++
		r.autoSwitches[from]++
		r.lastSwitch = time.Now()
		profile.WriteActivePointer(r.tool, cand)
		term.LogRotate("%s: %s → %s", reason, from, cand)
		return
	}
	term.LogWarn("ROTATE %s: %s → (all cooling/dead)", reason, from)
}

// ForceSwitch makes the named profile active immediately (the next request
// uses it). Used by `am switch` / the `/_am/switch` admin endpoint — unlike
// Rotate, this clears any cooldown/dead-refresh blacklist on the target,
// since a human explicitly picking that account is vouching for it.
func (r *Rotator) ForceSwitch(name string) error {
	return r.forceSwitch(name)
}

// ForceSwitchExplicit selects a profile for API X-Provider routing. Off
// profiles are still rejected — X-Provider is a routing hint, not a way to
// bypass `am off`; the caller must fail over elsewhere (or error) instead.
func (r *Rotator) ForceSwitchExplicit(name string) error {
	return r.forceSwitch(name)
}

func (r *Rotator) forceSwitch(name string) error {
	r.snapshotActiveIfChanged()

	r.mu.Lock()
	if len(r.order) == 0 {
		r.mu.Unlock()
		return fmt.Errorf("no %s profiles saved", r.tool)
	}
	from := r.order[r.idx]
	found := -1
	for i, n := range r.order {
		if n == name {
			found = i
		}
	}
	if found < 0 {
		order := make([]string, len(r.order))
		copy(order, r.order)
		r.mu.Unlock()
		return fmt.Errorf("no profile %q (have: %s)", name, strings.Join(order, ", "))
	}
	if r.disabled[name] {
		r.mu.Unlock()
		return fmt.Errorf("profile %q is off — run: am on %s", name, name)
	}
	r.idx = found
	target := r.order[r.idx]
	delete(r.cooldown, target) // manual switch clears any cooldown on the target
	delete(r.dead, target)     // ...and any dead-refresh blacklist; user is vouching for it
	r.switches++
	if target != from {
		r.manualSwitches[from]++
	}
	r.lastSwitch = time.Now()
	r.mu.Unlock()

	profile.WriteActivePointer(r.tool, target)
	if !profile.InstallActiveProfile(target) {
		r.mu.Lock()
		r.dead[target] = true
		r.mu.Unlock()
		return fmt.Errorf("refresh token for %q is dead — log into it again before switching to it", target)
	}
	term.LogSwitch("→ %s", target)
	return nil
}

// EvictDisabledActive switches away from the active profile if it was just
// turned off (`am off`). No-op when active is still enabled.
func (r *Rotator) EvictDisabledActive() {
	r.mu.Lock()
	if len(r.order) == 0 {
		r.mu.Unlock()
		return
	}
	cur := r.order[r.idx]
	if !r.disabled[cur] {
		r.mu.Unlock()
		return
	}
	r.mu.Unlock()
	if r.EnsureUsableActive() {
		return
	}
	log.Printf("amux: active profile %s is off and no other Claude account is usable", cur)
}

func (r *Rotator) Status() map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	accts := []map[string]any{}
	for _, n := range r.order {
		t := r.tokens[n]
		m := map[string]any{
			"profile":         n,
			"account":         r.accounts[n],
			"active":          len(r.order) > 0 && n == r.order[r.idx],
			"auto_switches":   r.autoSwitches[n],
			"manual_switches": r.manualSwitches[n],
		}
		if t != nil {
			m["remaining"] = t.Remaining
			m["token_expires"] = t.ExpiresAt
			if !t.ResetAt.IsZero() {
				m["limit_reset"] = t.ResetAt
			}
			if t.FiveH.Known {
				m["5h_used"] = t.FiveH.Used
				if !t.FiveH.ResetAt.IsZero() {
					m["5h_reset"] = t.FiveH.ResetAt
				}
			}
			if t.SevenD.Known {
				m["7d_used"] = t.SevenD.Used
				if !t.SevenD.ResetAt.IsZero() {
					m["7d_reset"] = t.SevenD.ResetAt
				}
			}
		}
		if cd, ok := r.cooldown[n]; ok && time.Now().Before(cd) {
			m["cooldown_until"] = cd
		}
		if r.dead[n] {
			m["dead"] = true
		}
		if r.disabled[n] {
			m["disabled"] = true
		}
		accts = append(accts, m)
	}
	return map[string]any{
		"tool":        r.tool,
		"switches":    r.switches,
		"last_switch": r.lastSwitch,
		"threshold":   int(r.usedThreshold*100 + 0.5),
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
