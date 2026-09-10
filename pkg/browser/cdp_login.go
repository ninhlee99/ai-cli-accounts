package browser

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// WebLoginTarget describes a site to open and which cookie to wait for.
type WebLoginTarget struct {
	Name       string // chatgpt | claude
	StartURL   string
	CookieHost string // substring match on cookie domain
	CookieName string
	Profile    string // subdir under ~/.am/browser-profiles
}

var (
	ChatGPTWebLogin = WebLoginTarget{
		Name:       "chatgpt",
		StartURL:   "https://chatgpt.com/",
		CookieHost: "chatgpt.com",
		CookieName: "__Secure-next-auth.session-token",
		Profile:    "chatgpt",
	}
	ClaudeWebLogin = WebLoginTarget{
		Name:       "claude",
		StartURL:   "https://claude.ai/login",
		CookieHost: "claude.ai",
		CookieName: "sessionKey",
		Profile:    "claude",
	}
	GeminiWebLogin = WebLoginTarget{
		Name:       "gemini",
		StartURL:   "https://aistudio.google.com/apikey",
		CookieHost: "google.com",
		CookieName: "__Secure-1PSID",
		Profile:    "gemini",
	}
)

// CapturedWebAuth is the session cookie plus a full Cookie header for the
// login host (Cloudflare / ancillary cookies included).
type CapturedWebAuth struct {
	SessionValue string
	CookieHeader string
}

// CaptureWebAuthViaBrowser opens a dedicated Chromium window (own profile under
// ~/.am/browser-profiles — NOT the system Chrome profile, NOT Keychain).
// User logs in in that window; we poll cookies over Chrome DevTools Protocol
// until CookieName appears, then return the session value and full Cookie header.
//
// No macOS Keychain calls. Cookies come from the live browser via CDP.
func CaptureWebAuthViaBrowser(target WebLoginTarget, timeout time.Duration) (*CapturedWebAuth, error) {
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	bin, err := findChromiumBinary()
	if err != nil {
		return nil, err
	}
	dir, err := profileDir(target.Profile)
	if err != nil {
		return nil, err
	}
	// Stale Singleton* from a prior hung login makes the new Chrome hand off
	// to a non-debug window — user logs in there, CDP never sees cookies.
	clearChromiumSingletonLocks(dir)

	port, err := pickFreePort()
	if err != nil {
		return nil, err
	}

	cmd := exec.Command(bin,
		fmt.Sprintf("--remote-debugging-port=%d", port),
		"--user-data-dir="+dir,
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-sync",
		"--new-window",
		target.StartURL,
	)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start browser: %w", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	deadline := time.Now().Add(timeout)
	var wsURL string
	for time.Now().Before(deadline) {
		wsURL, err = debuggerWSURL(port)
		if err == nil && wsURL != "" {
			break
		}
		time.Sleep(400 * time.Millisecond)
	}
	if wsURL == "" {
		return nil, fmt.Errorf("browser DevTools not ready on port %d: %v", port, err)
	}

	fmt.Printf("Browser opened (%s). Log in THERE (dedicated window) — waiting for %s (timeout %s)…\n",
		filepath.Base(bin), target.CookieName, timeout.Round(time.Second))
	fmt.Println("Tip: stay in the window amux opened (profile ~/.am/browser-profiles/" + target.Profile + ").")

	lastBeat := time.Now()
	for time.Now().Before(deadline) {
		cookies, gerr := cdpListCookies(wsURL)
		if gerr == nil {
			if val := pickSessionCookie(cookies, target); val != "" {
				return &CapturedWebAuth{
					SessionValue: val,
					CookieHeader: cookieHeaderForHost(cookies, target.CookieHost),
				}, nil
			}
		}
		if time.Since(lastBeat) >= 15*time.Second {
			left := time.Until(deadline).Round(time.Second)
			hint := summarizeAuthCookies(cookies, target.CookieHost)
			if gerr != nil {
				fmt.Printf("…still waiting (%s left); CDP: %v\n", left, gerr)
			} else if hint != "" {
				fmt.Printf("…still waiting (%s left); seen: %s\n", left, hint)
			} else {
				fmt.Printf("…still waiting (%s left); no auth cookie yet — finish login in the dedicated window\n", left)
			}
			lastBeat = time.Now()
		}
		time.Sleep(1200 * time.Millisecond)
	}
	return nil, fmt.Errorf("timed out waiting for %s after login — stay signed in in the amux window and retry", target.CookieName)
}

// RefreshWebAuthFromProfile opens the existing ~/.am/browser-profiles/<Profile>
// window briefly and returns session + full Cookie header if already logged in.
// Does not wait for interactive login (short timeout).
func RefreshWebAuthFromProfile(target WebLoginTarget, timeout time.Duration) (*CapturedWebAuth, error) {
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	bin, err := findChromiumBinary()
	if err != nil {
		return nil, err
	}
	dir, err := profileDir(target.Profile)
	if err != nil {
		return nil, err
	}
	clearChromiumSingletonLocks(dir)

	port, err := pickFreePort()
	if err != nil {
		return nil, err
	}

	startURL := "https://" + strings.TrimPrefix(target.CookieHost, ".") + "/"
	if target.CookieHost == "claude.ai" {
		startURL = "https://claude.ai/"
	}
	cmd := exec.Command(bin,
		fmt.Sprintf("--remote-debugging-port=%d", port),
		"--user-data-dir="+dir,
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-sync",
		"--new-window",
		startURL,
	)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start browser: %w", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	deadline := time.Now().Add(timeout)
	var wsURL string
	for time.Now().Before(deadline) {
		wsURL, err = debuggerWSURL(port)
		if err == nil && wsURL != "" {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if wsURL == "" {
		return nil, fmt.Errorf("browser DevTools not ready on port %d: %v", port, err)
	}

	// Give the profile a moment to load cookies into the jar.
	time.Sleep(800 * time.Millisecond)
	for time.Now().Before(deadline) {
		cookies, gerr := cdpListCookies(wsURL)
		if gerr == nil {
			if val := pickSessionCookie(cookies, target); val != "" {
				hdr := cookieHeaderForHost(cookies, target.CookieHost)
				if hdr == "" {
					hdr = target.CookieName + "=" + val
				}
				return &CapturedWebAuth{SessionValue: val, CookieHeader: hdr}, nil
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return nil, fmt.Errorf("no %s in profile %s — run am login %s", target.CookieName, target.Profile, target.Name)
}

// CaptureCookieViaBrowser opens a dedicated Chromium window and returns the
// session cookie value (see CaptureWebAuthViaBrowser).
func CaptureCookieViaBrowser(target WebLoginTarget, timeout time.Duration) (cookieValue string, err error) {
	auth, err := CaptureWebAuthViaBrowser(target, timeout)
	if err != nil {
		return "", err
	}
	return auth.SessionValue, nil
}

// cookieHeaderForHost builds a Cookie request header from CDP cookies whose
// domain matches hostSubstr (e.g. "claude.ai"). Includes Cloudflare /
// ancillary cookies that sessionKey alone does not cover.
func cookieHeaderForHost(cookies []cdpCookie, hostSubstr string) string {
	var parts []string
	seen := map[string]bool{}
	for _, c := range cookies {
		if c.Name == "" || c.Value == "" {
			continue
		}
		if hostSubstr != "" {
			d := strings.TrimPrefix(c.Domain, ".")
			if !strings.Contains(d, hostSubstr) && !strings.Contains(hostSubstr, d) {
				continue
			}
		}
		if seen[c.Name] {
			continue
		}
		seen[c.Name] = true
		parts = append(parts, c.Name+"="+c.Value)
	}
	return strings.Join(parts, "; ")
}

func clearChromiumSingletonLocks(dir string) {
	for _, name := range []string{"SingletonLock", "SingletonSocket", "SingletonCookie"} {
		_ = os.Remove(filepath.Join(dir, name))
	}
}

// pickSessionCookie returns the session cookie value for target.
// ChatGPT often splits the token across __Secure-next-auth.session-token.0/.1/.2.
func pickSessionCookie(cookies []cdpCookie, target WebLoginTarget) string {
	hostOK := func(domain string) bool {
		if target.CookieHost == "" {
			return true
		}
		d := strings.TrimPrefix(domain, ".")
		return strings.Contains(d, target.CookieHost) || strings.Contains(target.CookieHost, d)
	}

	// Exact name first.
	for _, c := range cookies {
		if !hostOK(c.Domain) {
			continue
		}
		if c.Name == target.CookieName && c.Value != "" {
			return c.Value
		}
	}

	// Chunked next-auth session token (.0 + .1 + .2 …) — must keep names;
	// concatenating raw values alone breaks /api/auth/session.
	if strings.Contains(target.CookieName, "session-token") {
		var hdr []string
		for i := 0; i < 8; i++ {
			want := fmt.Sprintf("%s.%d", target.CookieName, i)
			for _, c := range cookies {
				if !hostOK(c.Domain) {
					continue
				}
				if c.Name == want && c.Value != "" {
					hdr = append(hdr, want+"="+c.Value)
					break
				}
			}
		}
		if len(hdr) > 0 {
			return strings.Join(hdr, "; ")
		}
	}
	return ""
}

func summarizeAuthCookies(cookies []cdpCookie, hostSubstr string) string {
	var names []string
	seen := map[string]bool{}
	for _, c := range cookies {
		if hostSubstr != "" && !strings.Contains(c.Domain, hostSubstr) && !strings.Contains(c.Domain, "openai") {
			continue
		}
		n := c.Name
		if !(strings.Contains(n, "session") || strings.Contains(n, "auth") || strings.HasPrefix(n, "oai-") || strings.Contains(n, "token")) {
			continue
		}
		if seen[n] {
			continue
		}
		seen[n] = true
		names = append(names, n)
		if len(names) >= 6 {
			break
		}
	}
	return strings.Join(names, ", ")
}

func profileDir(name string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".am", "browser-profiles", name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

func findChromiumBinary() (string, error) {
	if runtime.GOOS != "darwin" {
		return "", fmt.Errorf("--browser login currently supports macOS only")
	}
	candidates := []string{
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Volumes/Macintosh HD/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
		"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
		"/Applications/Arc.app/Contents/MacOS/Arc",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c, nil
		}
	}
	if p, err := exec.LookPath("google-chrome"); err == nil {
		return p, nil
	}
	if p, err := exec.LookPath("chromium"); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("no Chrome/Edge/Brave/Arc found — install one, or paste --cookie/--token instead")
}

func pickFreePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

func debuggerWSURL(port int) (string, error) {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/json/version", port))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var meta struct {
		WS string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		return "", err
	}
	if meta.WS == "" {
		return "", fmt.Errorf("empty webSocketDebuggerUrl")
	}
	return meta.WS, nil
}

var cdpID atomic.Int64

type cdpCookie struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Domain string `json:"domain"`
}

func cdpListCookies(wsURL string) ([]cdpCookie, error) {
	cookies, err := cdpCallCookies(wsURL, "Network.getAllCookies")
	if err == nil && len(cookies) > 0 {
		return cookies, nil
	}
	// Chrome ≥122 browser target often prefers Storage.getCookies.
	alt, err2 := cdpCallCookies(wsURL, "Storage.getCookies")
	if err2 == nil {
		return alt, nil
	}
	if err != nil {
		return nil, err
	}
	return cookies, err2
}

func cdpCallCookies(wsURL, method string) ([]cdpCookie, error) {
	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	conn, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(8 * time.Second))
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))

	id := cdpID.Add(1)
	req := map[string]any{"id": id, "method": method}
	if err := conn.WriteJSON(req); err != nil {
		return nil, err
	}

	for {
		var msg struct {
			ID     int64 `json:"id"`
			Result struct {
				Cookies []cdpCookie `json:"cookies"`
			} `json:"result"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := conn.ReadJSON(&msg); err != nil {
			return nil, err
		}
		if msg.ID != id {
			continue
		}
		if msg.Error != nil {
			return nil, fmt.Errorf("cdp %s: %s", method, msg.Error.Message)
		}
		return msg.Result.Cookies, nil
	}
}
