package proxy

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"amux-accounts/pkg/types"
)

// authTokenPath is where the admin bearer token is persisted, 0600, generated
// on first --public bind. Loopback callers never need it (see requireAuth).
func authTokenPath() string {
	return filepath.Join(types.BaseDir(), "proxy.token")
}

// LoadOrCreateAuthToken returns the persisted admin token, generating a new
// 32-byte random one on first use. Required to authenticate /_am/* and
// /v1/* requests that arrive over a non-loopback interface (--public bind).
func LoadOrCreateAuthToken() (string, error) {
	p := authTokenPath()
	if b, err := os.ReadFile(p); err == nil {
		if tok := strings.TrimSpace(string(b)); tok != "" {
			return tok, nil
		}
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(buf)
	if err := os.MkdirAll(types.BaseDir(), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(p, []byte(tok+"\n"), 0o600); err != nil {
		return "", err
	}
	return tok, nil
}

// isLoopback reports whether r arrived over a loopback connection.
func isLoopback(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// requestToken extracts the bearer token from X-Am-Token or Authorization.
func requestToken(r *http.Request) string {
	if t := strings.TrimSpace(r.Header.Get("X-Am-Token")); t != "" {
		return t
	}
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
	}
	return ""
}

// requireAuth wraps h so non-loopback requests must present the admin token
// when the daemon is bound publicly (0.0.0.0/::). Loopback requests (the
// local CLI, Claude Code on the same machine) are never challenged, so
// existing local workflows keep working unauthenticated.
func requireAuth(token string, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token == "" || isLoopback(r) {
			h.ServeHTTP(w, r)
			return
		}
		got := requestToken(r)
		if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			http.Error(w, "amux proxy: missing or invalid token (see: am proxy token)", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	})
}
