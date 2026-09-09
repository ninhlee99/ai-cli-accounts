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
	KeychainService string
}

// KnownBrowsers returns standard Chromium profile paths on macOS.
func KnownBrowsers() []BrowserInfo {
	home, _ := os.UserHomeDir()
	return []BrowserInfo{
		{
			Name:            "Microsoft Edge",
			CookiePath:      filepath.Join(home, "Library/Application Support/Microsoft Edge/Default/Cookies"),
			KeychainService: "Microsoft Edge Safe Storage",
		},
		{
			Name:            "Google Chrome",
			CookiePath:      filepath.Join(home, "Library/Application Support/Google/Chrome/Default/Cookies"),
			KeychainService: "Chrome Safe Storage",
		},
		{
			Name:            "Arc",
			CookiePath:      filepath.Join(home, "Library/Application Support/Arc/User Data/Default/Cookies"),
			KeychainService: "Arc Safe Storage",
		},
		{
			Name:            "Brave",
			CookiePath:      filepath.Join(home, "Library/Application Support/BraveSoftware/Brave-Browser/Default/Cookies"),
			KeychainService: "Brave Safe Storage",
		},
	}
}

// ExtractCookie attempts to find and decrypt a cookie with cookieName matching domainFilter from any installed browser.
func ExtractCookie(domainFilter, cookieName string) (string, string, error) {
	for _, b := range KnownBrowsers() {
		if _, err := os.Stat(b.CookiePath); err != nil {
			continue
		}
		val, err := readCookieFromBrowser(b, domainFilter, cookieName)
		if err == nil && val != "" {
			return val, b.Name, nil
		}
	}
	return "", "", fmt.Errorf("cookie %q for %q not found in any browser", cookieName, domainFilter)
}

func readCookieFromBrowser(b BrowserInfo, domainFilter, cookieName string) (string, error) {
	// 1. Get keychain password
	cmd := exec.Command("security", "find-generic-password", "-s", b.KeychainService, "-w")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("keychain %s: %w", b.KeychainService, err)
	}
	pass := strings.TrimSpace(string(out))
	key := pbkdf2.Key([]byte(pass), []byte("saltysalt"), 1003, 16, sha1.New)

	// 2. Make temporary copy of Cookies SQLite database (to prevent database locked errors)
	tmp := filepath.Join(os.TempDir(), fmt.Sprintf("am_cookie_%d.db", time.Now().UnixNano()))
	defer os.Remove(tmp)

	data, err := os.ReadFile(b.CookiePath)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return "", err
	}

	// 3. Query sqlite3 CLI for matching cookie
	query := fmt.Sprintf("SELECT name, hex(encrypted_value) FROM cookies WHERE host_key LIKE '%%%s%%' AND name LIKE '%s%%' ORDER BY name ASC;", domainFilter, cookieName)
	sqlCmd := exec.Command("/usr/bin/sqlite3", tmp, query)
	sqlOut, err := sqlCmd.Output()
	if err != nil {
		return "", fmt.Errorf("sqlite3 query: %w", err)
	}

	var combined strings.Builder
	lines := strings.Split(strings.TrimSpace(string(sqlOut)), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) != 2 {
			continue
		}
		name := parts[0]
		encBytes, err := hex.DecodeString(parts[1])
		if err != nil {
			continue
		}
		dec := decryptCookie(key, encBytes)
		if dec == "" {
			continue
		}
		if name == cookieName {
			return dec, nil
		}
		// Some cookies (like __Secure-next-auth.session-token.0 and .1) are split across multiple chunks
		if strings.HasPrefix(name, cookieName) {
			combined.WriteString(dec)
		}
	}

	if combined.Len() > 0 {
		return combined.String(), nil
	}
	return "", fmt.Errorf("cookie %s not found", cookieName)
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
	iv := bytes.Repeat([]byte(" "), aes.BlockSize) // 16 spaces
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

// FetchChatGPTSessionAccessToken exchanges next-auth session-token cookie for an accessToken.
func FetchChatGPTSessionAccessToken(sessionToken string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, "https://chatgpt.com/api/auth/session", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Cookie", fmt.Sprintf("__Secure-next-auth.session-token=%s", sessionToken))
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("session endpoint returned status %d: %s", resp.StatusCode, string(b))
	}

	var data struct {
		AccessToken string `json:"accessToken"`
		Error       string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", err
	}
	if data.AccessToken == "" {
		return "", fmt.Errorf("no accessToken in session response: %s", data.Error)
	}
	return data.AccessToken, nil
}
