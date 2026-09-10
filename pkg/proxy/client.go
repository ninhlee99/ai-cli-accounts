package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"amux-accounts/pkg/profile"
)

// proxyAddr is the address clients should reach the proxy on: AM_PROXY_ADDR
// if set, else the same "127.0.0.1:8787" default pkg/cli/cli.go's "proxy"
// case binds to when --addr isn't passed.
func proxyAddr() string {
	addr := os.Getenv("AM_PROXY_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8787"
	}
	return addr
}

func ProxyBase() string {
	return "http://" + proxyAddr()
}

func ProxyUp() bool {
	c := http.Client{Timeout: 500 * time.Millisecond}
	resp, err := c.Get(ProxyBase() + "/_am/status")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// resolveAMBin finds the am binary to re-exec for a detached child process
// (the proxy server itself, or its supervisor). Prefers the currently
// running executable's own path: it's guaranteed to be a binary that just
// ran successfully (this call), whereas a stray, stale, or otherwise broken
// "am" earlier on PATH would get spawned instead and could fail every time
// (observed: a leftover ~/.local/bin/am got signal-killed on every exec in
// one environment, silently sending the supervisor into an unbreakable
// crash-loop). Falls back to PATH only if the running executable's path
// can't be determined.
func resolveAMBin() (string, error) {
	if self, err := os.Executable(); err == nil {
		return self, nil
	}
	if bin, err := exec.LookPath("am"); err == nil {
		return bin, nil
	}
	return "", fmt.Errorf("could not resolve am binary")
}

// proxyStatusMode returns the "mode" field from a running proxy's
// /_am/status ("degraded" when RunSupervisor's crash-loop fallback is
// serving instead of the full server; "" if unreachable or absent).
func proxyStatusMode() string {
	resp, err := http.Get(ProxyBase() + "/_am/status")
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var s struct {
		Mode string `json:"mode"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return ""
	}
	return s.Mode
}

func CmdProxyUp(threshold ...float64) {
	thresh := DefaultUsedThreshold
	if len(threshold) > 0 && threshold[0] > 0 {
		thresh = ParseUsedThreshold(threshold[0])
	} else if env := os.Getenv("AM_ROTATE_THRESHOLD"); env != "" {
		if f, err := strconv.ParseFloat(env, 64); err == nil {
			thresh = ParseUsedThreshold(f)
		}
	}
	SetUsedThreshold(thresh)

	needSpawn := !ProxyUp()
	if !needSpawn && proxyStatusMode() == "degraded" {
		// A crash-looped supervisor (see RunSupervisor) left a bare
		// Anthropic-direct listener bound to the port instead of the full
		// server. Ask it to step aside and spawn a fresh supervised one.
		postAndClose(ProxyBase() + "/_am/shutdown")
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) && ProxyUp() {
			time.Sleep(50 * time.Millisecond)
		}
		needSpawn = true
	}
	if needSpawn {
		bin, err := resolveAMBin()
		if err != nil {
			fmt.Fprintf(os.Stderr, "amux: %v\n", err)
			return
		}
		// Pass --addr / --threshold explicitly so the spawned supervisor
		// binds the same address CmdProxyUp/ProxyUp just checked and
		// inherits the rotate threshold (AM_PROXY_ADDR / AM_ROTATE_THRESHOLD
		// alone aren't enough — child argv is the source of truth).
		cmd := exec.Command(bin, "proxy", "--supervise",
			"--addr", proxyAddr(),
			"--threshold", strconv.FormatFloat(thresh*100, 'f', -1, 64),
		)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := cmd.Start(); err != nil {
			fmt.Fprintf(os.Stderr, "amux: start proxy: %v\n", err)
			return
		}
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if ProxyUp() {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
	}

	ppid := os.Getppid()
	if ppid > 1 {
		// Send both the current ("event") and legacy ("op") param names: a
		// background `am proxy` process started by an older `am` binary
		// (still running, unaffected by upgrading the binary on disk) only
		// understands "op". Sending both means this hook doesn't silently
		// stop tracking sessions against a stale proxy after an upgrade.
		postAndClose(fmt.Sprintf("%s/_am/session?pid=%d&event=start&op=start", ProxyBase(), ppid))
	}
	postAndClose(ProxyBase() + "/_am/sync")
}

// Sync hot-reloads the running proxy daemon's provider pool (and active
// profile) from disk, if one is up. No-op when the proxy isn't running —
// callers that also mutate accounts.json/profiles directly on disk don't
// need to do anything else, the next `am proxy up` picks up the change too.
func Sync() {
	if ProxyUp() {
		postAndClose(ProxyBase() + "/_am/sync")
	}
}

func postAndClose(url string) {
	resp, err := http.Post(url, "", nil)
	if err == nil && resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
}

func attachedSessions() int {
	resp, err := http.Get(ProxyBase() + "/_am/status")
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

func CmdProxyDown(force, yesIKnow bool) {
	if !ProxyUp() {
		fmt.Println("amux proxy is not running")
		return
	}
	if force {
		if !yesIKnow {
			if n := attachedSessions(); n != 0 {
				if n < 0 {
					fmt.Fprintln(os.Stderr, "amux: couldn't confirm 0 claude tab(s) attached (status check failed) — "+
						"stopping now risks leaving one pointed at a dead port. "+
						"`amux proxy down --force --yes-i-know` to stop anyway.")
					return
				}
				fmt.Fprintf(os.Stderr, "amux: %d claude tab(s) still attached — stopping now leaves them pointed at a dead port "+
					"(ANTHROPIC_BASE_URL is fixed for the life of that process). "+
					"Close those tabs first, or `amux proxy down --force --yes-i-know` to stop anyway.\n", n)
				return
			}
		}
		postAndClose(ProxyBase() + "/_am/shutdown")
		fmt.Println("amux proxy stopped")
		return
	}

	ppid := os.Getppid()
	if ppid > 1 {
		// See CmdProxyUp: send both param names for the same stale-proxy
		// reason.
		postAndClose(fmt.Sprintf("%s/_am/session?pid=%d&event=end&op=end", ProxyBase(), ppid))
		return
	}
	postAndClose(ProxyBase() + "/_am/shutdown")
	fmt.Println("amux proxy stopped")
}

func CmdSwitch(tool, name string) {
	if tool != "claude" {
		if err := profile.CmdUse(tool, name); err != nil {
			fmt.Fprintf(os.Stderr, "amux: %v\n", err)
		}
		return
	}

	if !ProxyUp() {
		if err := profile.CmdUse(tool, name); err != nil {
			fmt.Fprintf(os.Stderr, "amux: %v\n", err)
		}
		return
	}

	resp, err := http.Post(ProxyBase()+"/_am/switch?to="+url.QueryEscape(name), "", nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "amux: proxy switch: %v\n", err)
		return
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "amux: proxy switch failed: %s\n", strings.TrimSpace(string(b)))
		return
	}
	fmt.Printf("switched to %s (no restart needed)\n", name)
}

func CmdSwitchProvider(name string) {
	if !ProxyUp() {
		fmt.Fprintf(os.Stderr, "amux: proxy not running — start a session or run 'amux proxy' first\n")
		return
	}
	resp, err := http.Post(ProxyBase()+"/_am/switch-provider?to="+url.QueryEscape(name), "", nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "amux: proxy switch: %v\n", err)
		return
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "amux: proxy switch failed: %s\n", strings.TrimSpace(string(b)))
		return
	}
	fmt.Printf("switched Claude Code traffic to provider %q (no restart needed)\n", name)
}
