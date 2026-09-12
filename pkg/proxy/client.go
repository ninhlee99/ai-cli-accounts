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

func ProxyBase() string {
	return "http://" + ProxyAddr()
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

func resolveAMBin() (string, error) {
	if self, err := os.Executable(); err == nil {
		return self, nil
	}
	if bin, err := exec.LookPath("am"); err == nil {
		return bin, nil
	}
	return "", fmt.Errorf("could not resolve am binary")
}

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

// UpFlags controls `amux proxy up` / `amux proxy --public` behaviour.
type UpFlags struct {
	Threshold float64 // 0 → default / env
	Public    *bool   // nil keep saved; non-nil persist + restart
	Restart   bool    // force respawn even if already up
}

func CmdProxyUp(threshold ...float64) {
	f := UpFlags{}
	if len(threshold) > 0 && threshold[0] > 0 {
		f.Threshold = threshold[0]
	}
	CmdProxyUpFlags(f)
}

func CmdProxyUpFlags(f UpFlags) {
	thresh := DefaultUsedThreshold
	if f.Threshold > 0 {
		thresh = ParseUsedThreshold(f.Threshold)
	} else if env := os.Getenv("AM_ROTATE_THRESHOLD"); env != "" {
		if v, err := strconv.ParseFloat(env, 64); err == nil {
			thresh = ParseUsedThreshold(v)
		}
	}
	SetUsedThreshold(thresh)

	if f.Public != nil {
		if err := SaveBindPublic(*f.Public); err != nil {
			fmt.Fprintf(os.Stderr, "amux: save bind preference: %v\n", err)
		}
		f.Restart = true
	}

	needSpawn := !ProxyUp() || f.Restart
	if !needSpawn && proxyStatusMode() == "degraded" {
		postAndClose(ProxyBase() + "/_am/shutdown")
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) && ProxyUp() {
			time.Sleep(50 * time.Millisecond)
		}
		needSpawn = true
	}
	if f.Restart && ProxyUp() {
		postAndClose(ProxyBase() + "/_am/shutdown")
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) && ProxyUp() {
			time.Sleep(50 * time.Millisecond)
		}
		needSpawn = true
	}

	listen := ListenAddr()
	if needSpawn {
		bin, err := resolveAMBin()
		if err != nil {
			fmt.Fprintf(os.Stderr, "amux: %v\n", err)
			return
		}
		cmd := exec.Command(bin, "proxy", "--supervise",
			"--addr", listen,
			"--threshold", strconv.FormatFloat(thresh*100, 'f', -1, 64),
		)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := cmd.Start(); err != nil {
			fmt.Fprintf(os.Stderr, "amux: start proxy: %v\n", err)
			return
		}
		deadline := time.Now().Add(4 * time.Second)
		for time.Now().Before(deadline) {
			if ProxyUp() {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if !ProxyUp() {
			fmt.Fprintf(os.Stderr, "amux: proxy did not come up on %s (clients: %s)\n", listen, ProxyAddr())
			return
		}
	}

	ppid := os.Getppid()
	if ppid > 1 {
		postAndClose(fmt.Sprintf("%s/_am/session?pid=%d&event=start&op=start", ProxyBase(), ppid))
	}
	postAndClose(ProxyBase() + "/_am/sync")

	if IsPublic() || IsPublicBind(listen) {
		fmt.Printf("amux proxy up  bind %s  local %s\n", listen, ProxyBase())
		fmt.Printf("  public  %s\n", FormatPublicHosts())
	} else if f.Restart || needSpawn {
		fmt.Printf("amux proxy up  bind %s  %s\n", listen, ProxyBase())
	}
}

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
