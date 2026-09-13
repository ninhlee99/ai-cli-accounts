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
	"sync"
	"time"

	"amux-accounts/pkg/types"
)

// AuthTokenPrefix is the standard prefix for amux public proxy API keys.
const AuthTokenPrefix = "amux-"

// authTokenPath is where the admin bearer token is persisted, 0600, generated
// when the proxy binds publicly. Loopback callers never need it (see requireAuth).
func authTokenPath() string {
	return filepath.Join(types.BaseDir(), "proxy.token")
}

// IssueNewAuthToken creates a fresh API key with format amux-<token>, persists
// it to disk (0600) overwriting any previous key, and returns it. Each time
// a public proxy is launched, a brand-new ephemeral key is issued.
func IssueNewAuthToken() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	tok := AuthTokenPrefix + hex.EncodeToString(buf)
	p := authTokenPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(p, []byte(tok+"\n"), 0o600); err != nil {
		return "", err
	}
	return tok, nil
}

// LoadAuthToken reads the currently persisted token from disk.
func LoadAuthToken() (string, error) {
	p := authTokenPath()
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	tok := strings.TrimSpace(string(b))
	if tok == "" {
		return "", os.ErrNotExist
	}
	return tok, nil
}

// ClearAuthToken removes the persisted token file when the public proxy is stopped.
func ClearAuthToken() error {
	p := authTokenPath()
	err := os.Remove(p)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// LoadOrCreateAuthToken returns the persisted amux admin token, generating a new
// one with amux- prefix if none exists or if the existing one is invalid.
func LoadOrCreateAuthToken() (string, error) {
	if tok, err := LoadAuthToken(); err == nil && strings.HasPrefix(tok, AuthTokenPrefix) {
		return tok, nil
	}
	return IssueNewAuthToken()
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

// requestToken extracts the bearer token from X-Am-Token, X-Api-Key, x-goog-api-key, api-key, Authorization, or ?key=.
func requestToken(r *http.Request) string {
	if t := strings.TrimSpace(r.Header.Get("X-Am-Token")); t != "" {
		return t
	}
	if t := strings.TrimSpace(r.Header.Get("X-Api-Key")); t != "" {
		return t
	}
	if t := strings.TrimSpace(r.Header.Get("x-goog-api-key")); t != "" {
		return t
	}
	if t := strings.TrimSpace(r.Header.Get("api-key")); t != "" {
		return t
	}
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
	}
	if key := strings.TrimSpace(r.URL.Query().Get("key")); key != "" {
		return key
	}
	return ""
}

var (
	authLimiterMu   sync.Mutex
	failedAttempts  = make(map[string][]time.Time)
	authLimiterStop = make(chan struct{})
	authLimiterOnce sync.Once
)

const (
	maxFailedAuthAttempts = 10
	failedAuthWindow      = 1 * time.Minute
	// maxFailedAuthPerIP caps the timestamps tracked for a single IP to prevent
	// slice memory amplification under high-volume brute force attacks.
	maxFailedAuthPerIP = 20
	// maxFailedAuthIPs caps in-memory state to prevent OOM when an attacker
	// floods the server from many unique source IPs (e.g. 100k+).
	// When the cap is reached, an expired or older entry is evicted via sampling.
	//
	// NOTE: This limiter keys on the raw socket RemoteAddr only — it does NOT
	// read X-Forwarded-For or similar headers. If the proxy is deployed behind
	// a reverse proxy (nginx, Caddy, etc.) all traffic will appear as 127.0.0.1
	// and rate limiting will not work correctly. In that topology, configure
	// rate limiting at the reverse-proxy layer instead.
	maxFailedAuthIPs = 50_000
)

// StopAuthRateLimiter signals the background eviction goroutine to exit cleanly.
// Call this during graceful server shutdown to avoid goroutine leaks.
// Safe to call multiple times (idempotent via sync.Once).
func StopAuthRateLimiter() {
	authLimiterOnce.Do(func() { close(authLimiterStop) })
}

func init() {
	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				authLimiterMu.Lock()
				cutoff := time.Now().Add(-failedAuthWindow)
				for ip, times := range failedAttempts {
					valid := times[:0]
					for _, t := range times {
						if t.After(cutoff) {
							valid = append(valid, t)
						}
					}
					if len(valid) == 0 {
						delete(failedAttempts, ip)
					} else {
						failedAttempts[ip] = valid
					}
				}
				authLimiterMu.Unlock()
			case <-authLimiterStop:
				return
			}
		}
	}()
}

func isAuthRateLimited(ip string) bool {
	authLimiterMu.Lock()
	defer authLimiterMu.Unlock()

	cutoff := time.Now().Add(-failedAuthWindow)
	times := failedAttempts[ip]
	valid := times[:0]
	for _, t := range times {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}
	if len(valid) == 0 {
		delete(failedAttempts, ip)
		return false
	}
	failedAttempts[ip] = valid
	return len(valid) >= maxFailedAuthAttempts
}

func recordFailedAuth(ip string) {
	authLimiterMu.Lock()
	defer authLimiterMu.Unlock()

	// When at capacity, sample up to 16 entries and evict an expired entry
	// (or the oldest observed) to protect active rate-limited IPs from being evicted.
	if _, exists := failedAttempts[ip]; !exists && len(failedAttempts) >= maxFailedAuthIPs {
		cutoff := time.Now().Add(-failedAuthWindow)
		var oldestKey string
		var oldestTime time.Time
		sampleCount := 0
		for k, times := range failedAttempts {
			sampleCount++
			if len(times) == 0 || times[len(times)-1].Before(cutoff) {
				oldestKey = k
				break // Evict expired entry immediately
			}
			newest := times[len(times)-1]
			if oldestKey == "" || newest.Before(oldestTime) {
				oldestKey = k
				oldestTime = newest
			}
			if sampleCount >= 16 {
				break
			}
		}
		if oldestKey != "" {
			delete(failedAttempts, oldestKey)
		}
	}

	// Bound memory per IP: cap slice to maxFailedAuthPerIP with FIFO shift
	times := failedAttempts[ip]
	now := time.Now()
	if len(times) < maxFailedAuthPerIP {
		failedAttempts[ip] = append(times, now)
	} else {
		copy(times, times[1:])
		times[len(times)-1] = now
		failedAttempts[ip] = times
	}
}

func recordSuccessfulAuth(ip string) {
	authLimiterMu.Lock()
	defer authLimiterMu.Unlock()
	delete(failedAttempts, ip)
}

// requireAuth wraps h so non-loopback requests must present the valid amux-<auth-token>
// when the daemon is bound publicly (0.0.0.0/:: or external IP). Loopback requests (the
// local CLI, Claude Code on the same machine) are never challenged, so existing local
// workflows keep working with dummy/sample keys or unauthenticated.
//
// On public hosts, sample keys, dummy keys, or arbitrary strings are strictly rejected;
// only the ephemeral key issued for that public proxy run is accepted.
// Also includes rate-limiting to protect against brute-force attacks.
func requireAuth(token string, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token == "" || isLoopback(r) {
			h.ServeHTTP(w, r)
			return
		}
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		if isAuthRateLimited(host) {
			http.Error(w, "amux proxy: too many failed authentication attempts — rate limited (try again later)", http.StatusTooManyRequests)
			return
		}

		got := requestToken(r)
		if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			recordFailedAuth(host)
			http.Error(w, "amux proxy: unauthorized — public host requires valid API key with format amux-<auth-token> (see: am proxy token)", http.StatusUnauthorized)
			return
		}
		recordSuccessfulAuth(host)
		h.ServeHTTP(w, r)
	})
}
