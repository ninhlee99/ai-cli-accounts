package browser

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/pbkdf2"
)

type BrowserInfo struct {
	Name            string
	CookiePath      string
	KeychainService string // empty = no keychain (Firefox plaintext)
}

// KnownBrowsers returns browser cookie stores. Firefox is listed first —
// its moz_cookies.value column is plaintext, so login works without
// touching macOS Keychain. Chromium browsers need Safe Storage keychain
// only to decrypt the Cookies SQLite blob.
func KnownBrowsers() []BrowserInfo {
	home, _ := os.UserHomeDir()
	out := []BrowserInfo{}
	ffGlob := filepath.Join(home, "Library/Application Support/Firefox/Profiles/*/cookies.sqlite")
	if matches, _ := filepath.Glob(ffGlob); len(matches) > 0 {
		for _, m := range matches {
			out = append(out, BrowserInfo{Name: "Firefox", CookiePath: m})
		}
	}
	out = append(out,
		BrowserInfo{
			Name:            "Microsoft Edge",
			CookiePath:      filepath.Join(home, "Library/Application Support/Microsoft Edge/Default/Cookies"),
			KeychainService: "Microsoft Edge Safe Storage",
		},
		BrowserInfo{
			Name:            "Google Chrome",
			CookiePath:      filepath.Join(home, "Library/Application Support/Google/Chrome/Default/Cookies"),
			KeychainService: "Chrome Safe Storage",
		},
		BrowserInfo{
			Name:            "Arc",
			CookiePath:      filepath.Join(home, "Library/Application Support/Arc/User Data/Default/Cookies"),
			KeychainService: "Arc Safe Storage",
		},
		BrowserInfo{
			Name:            "Brave",
			CookiePath:      filepath.Join(home, "Library/Application Support/BraveSoftware/Brave-Browser/Default/Cookies"),
			KeychainService: "Brave Safe Storage",
		},
	)
	return out
}

// ExtractCookie finds cookieName for domainFilter. Prefers Firefox
// (no keychain). Chromium decrypt is attempted next and may fail if the
// user hasn't granted Safe Storage keychain access.
func ExtractCookie(domainFilter, cookieName string) (string, string, error) {
	var lastErr error
	for _, b := range KnownBrowsers() {
		if _, err := os.Stat(b.CookiePath); err != nil {
			continue
		}
		var val string
		var err error
		if b.KeychainService == "" {
			val, err = readFirefoxCookie(b.CookiePath, domainFilter, cookieName)
		} else {
			val, err = readChromiumCookie(b, domainFilter, cookieName)
		}
		if err != nil {
			lastErr = err
			continue
		}
		if val != "" {
			return val, b.Name, nil
		}
	}
	if lastErr != nil {
		return "", "", fmt.Errorf("cookie %q for %q not found: %w", cookieName, domainFilter, lastErr)
	}
	return "", "", fmt.Errorf("cookie %q for %q not found in any browser", cookieName, domainFilter)
}

// ParseCookieHeader pulls one named cookie out of a raw Cookie header
// ("a=1; b=2") or a single "name=value" paste from DevTools.
func ParseCookieHeader(raw, cookieName string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	raw = strings.TrimPrefix(raw, "Cookie:")
	raw = strings.TrimSpace(raw)
	for _, part := range strings.Split(raw, ";") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, cookieName+"=") {
			return strings.TrimPrefix(part, cookieName+"=")
		}
	}
	// bare value paste (no name=)
	if !strings.Contains(raw, "=") && !strings.Contains(raw, ";") {
		return raw
	}
	return ""
}

func readFirefoxCookie(dbPath, domainFilter, cookieName string) (string, error) {
	tmp := filepath.Join(os.TempDir(), fmt.Sprintf("am_ff_cookie_%d.db", time.Now().UnixNano()))
	defer os.Remove(tmp)
	data, err := os.ReadFile(dbPath)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return "", err
	}
	query := fmt.Sprintf(
		`SELECT name, value FROM moz_cookies WHERE host LIKE '%%%s%%' AND name LIKE '%s%%' ORDER BY lastAccessed DESC;`,
		escapeSQLLike(domainFilter), escapeSQLLike(cookieName),
	)
	sqlOut, err := exec.Command("/usr/bin/sqlite3", tmp, query).Output()
	if err != nil {
		return "", fmt.Errorf("firefox sqlite3: %w", err)
	}
	var combined strings.Builder
	for _, line := range strings.Split(strings.TrimSpace(string(sqlOut)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 2)
		if len(parts) != 2 || parts[1] == "" {
			continue
		}
		if parts[0] == cookieName {
			return parts[1], nil
		}
		if strings.HasPrefix(parts[0], cookieName) {
			combined.WriteString(parts[1])
		}
	}
	if combined.Len() > 0 {
		return combined.String(), nil
	}
	return "", fmt.Errorf("firefox: cookie %s not found", cookieName)
}

func readChromiumCookie(b BrowserInfo, domainFilter, cookieName string) (string, error) {
	cmd := exec.Command("security", "find-generic-password", "-s", b.KeychainService, "-w")
	out, err := cmd.Output()
	if err != nil || strings.TrimSpace(string(out)) == "" {
		return "", fmt.Errorf("%s Safe Storage unavailable (grant keychain access or paste cookie instead)", b.Name)
	}
	pass := strings.TrimSpace(string(out))
	key := pbkdf2.Key([]byte(pass), []byte("saltysalt"), 1003, 16, sha1.New)

	tmp := filepath.Join(os.TempDir(), fmt.Sprintf("am_cookie_%d.db", time.Now().UnixNano()))
	defer os.Remove(tmp)
	data, err := os.ReadFile(b.CookiePath)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return "", err
	}

	query := fmt.Sprintf(
		`SELECT name, hex(encrypted_value) FROM cookies WHERE host_key LIKE '%%%s%%' AND name LIKE '%s%%' ORDER BY name ASC;`,
		escapeSQLLike(domainFilter), escapeSQLLike(cookieName),
	)
	sqlOut, err := exec.Command("/usr/bin/sqlite3", tmp, query).Output()
	if err != nil {
		return "", fmt.Errorf("sqlite3 query: %w", err)
	}

	var combined strings.Builder
	for _, line := range strings.Split(strings.TrimSpace(string(sqlOut)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) != 2 {
			continue
		}
		encBytes, err := hex.DecodeString(parts[1])
		if err != nil {
			continue
		}
		dec := decryptCookie(key, encBytes)
		if dec == "" {
			continue
		}
		if parts[0] == cookieName {
			return dec, nil
		}
		if strings.HasPrefix(parts[0], cookieName) {
			combined.WriteString(dec)
		}
	}
	if combined.Len() > 0 {
		return combined.String(), nil
	}
	return "", fmt.Errorf("cookie %s not found", cookieName)
}

func escapeSQLLike(s string) string {
	s = strings.ReplaceAll(s, `'`, `''`)
	return s
}

func decryptCookie(key, encVal []byte) string {
	if len(encVal) < 3 || string(encVal[:3]) != "v10" {
		return string(encVal)
	}
	data := encVal[3:]
	if len(data)%aes.BlockSize != 0 {
		return ""
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return ""
	}
	iv := bytes.Repeat([]byte(" "), aes.BlockSize)
	mode := cipher.NewCBCDecrypter(block, iv)
	dec := make([]byte, len(data))
	mode.CryptBlocks(dec, data)
	if len(dec) == 0 {
		return ""
	}
	pad := int(dec[len(dec)-1])
	if pad > 0 && pad <= aes.BlockSize && len(dec) >= pad {
		dec = dec[:len(dec)-pad]
	}
	return string(dec)
}

// ChatGPTSession is the useful subset of GET /api/auth/session.
type ChatGPTSession struct {
	AccessToken  string
	RefreshToken string
	Expires      string
	Email        string
}

// FetchChatGPTSession exchanges a next-auth session-token cookie (or any
// Cookie header that authenticates chatgpt.com) for access/refresh tokens
// from the web session endpoint — no keychain involved.
func FetchChatGPTSession(sessionTokenOrCookie string) (*ChatGPTSession, error) {
	cookie := sessionTokenOrCookie
	if !strings.Contains(cookie, "=") {
		cookie = "__Secure-next-auth.session-token=" + sessionTokenOrCookie
	}

	req, err := http.NewRequest(http.MethodGet, "https://chatgpt.com/api/auth/session", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Cookie", cookie)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("session endpoint status %d: %s", resp.StatusCode, bytes.TrimSpace(body))
	}

	var data struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		Expires      string `json:"expires"`
		Error        string `json:"error"`
		User         struct {
			Email string `json:"email"`
		} `json:"user"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	if data.AccessToken == "" {
		return nil, fmt.Errorf("no accessToken in session response: %s", data.Error)
	}
	return &ChatGPTSession{
		AccessToken:  data.AccessToken,
		RefreshToken: data.RefreshToken,
		Expires:      data.Expires,
		Email:        data.User.Email,
	}, nil
}

// FetchChatGPTSessionAccessToken keeps the old single-token helper.
func FetchChatGPTSessionAccessToken(sessionToken string) (string, error) {
	s, err := FetchChatGPTSession(sessionToken)
	if err != nil {
		return "", err
	}
	return s.AccessToken, nil
}
