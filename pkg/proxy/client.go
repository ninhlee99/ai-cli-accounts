package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"ai-cli-accounts/pkg/profile"
)

func ProxyBase() string {
	addr := os.Getenv("AM_PROXY_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8787"
	}
	return "http://" + addr
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

func CmdProxyUp() {
	if !ProxyUp() {
		bin, err := exec.LookPath("am")
		if err != nil {
			if self, e := os.Executable(); e == nil {
				bin = self
			} else {
				fmt.Fprintf(os.Stderr, "am: could not resolve am binary: %v\n", err)
				return
			}
		}
		cmd := exec.Command(bin, "proxy")
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := cmd.Start(); err != nil {
			fmt.Fprintf(os.Stderr, "am: start proxy: %v\n", err)
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
		fmt.Println("am proxy is not running")
		return
	}
	if force {
		if !yesIKnow {
			if n := attachedSessions(); n != 0 {
				if n < 0 {
					fmt.Fprintln(os.Stderr, "am: couldn't confirm 0 claude tab(s) attached (status check failed) — "+
						"stopping now risks leaving one pointed at a dead port. "+
						"`am proxy down --force --yes-i-know` to stop anyway.")
					return
				}
				fmt.Fprintf(os.Stderr, "am: %d claude tab(s) still attached — stopping now leaves them pointed at a dead port "+
					"(ANTHROPIC_BASE_URL is fixed for the life of that process). "+
					"Close those tabs first, or `am proxy down --force --yes-i-know` to stop anyway.\n", n)
				return
			}
		}
		postAndClose(ProxyBase() + "/_am/shutdown")
		fmt.Println("am proxy stopped")
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
	fmt.Println("am proxy stopped")
}

func CmdSwitch(tool, name string) {
	if tool != "claude" {
		if err := profile.CmdUse(tool, name); err != nil {
			fmt.Fprintf(os.Stderr, "am: %v\n", err)
		}
		return
	}

	if !ProxyUp() {
		if err := profile.CmdUse(tool, name); err != nil {
			fmt.Fprintf(os.Stderr, "am: %v\n", err)
		}
		return
	}

	resp, err := http.Post(ProxyBase()+"/_am/switch?to="+url.QueryEscape(name), "", nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "am: proxy switch: %v\n", err)
		return
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "am: proxy switch failed: %s\n", strings.TrimSpace(string(b)))
		return
	}
	fmt.Printf("switched to %s (no restart needed)\n", name)
}

func CmdSwitchProvider(name string) {
	if !ProxyUp() {
		fmt.Fprintf(os.Stderr, "am: proxy not running — start a session or run 'am proxy' first\n")
		return
	}
	resp, err := http.Post(ProxyBase()+"/_am/switch-provider?to="+url.QueryEscape(name), "", nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "am: proxy switch: %v\n", err)
		return
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "am: proxy switch failed: %s\n", strings.TrimSpace(string(b)))
		return
	}
	fmt.Printf("switched Claude Code traffic to provider %q (no restart needed)\n", name)
}
