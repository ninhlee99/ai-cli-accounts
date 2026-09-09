package main

import (
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// am usage --by-project needs to know which claude tab a request came from.
// All tabs share one proxy port, so requests carry no project identity of
// their own — but each tab holds its own TCP connection to the proxy, and
// the OS can map that connection's client-side ephemeral port back to the
// owning process, then that process's cwd. Both lookups go through lsof
// (macOS has no /proc): slow-ish (~10-30ms) so results are cached per
// remote address for a while, since a connection is held open and reused
// for many requests.

type projectCache struct {
	mu     sync.Mutex
	byAddr map[string]cachedProject
	ourPID int
}

type cachedProject struct {
	dir     string
	expires time.Time
}

var projCache = &projectCache{
	byAddr: map[string]cachedProject{},
	ourPID: os.Getpid(),
}

const projectCacheTTL = 2 * time.Minute

// projectForRemoteAddr resolves a client connection's cwd (e.g.
// "/Users/x/code/foo" -> "foo") from its RemoteAddr ("127.0.0.1:65022").
// Returns "" on any failure — this is best-effort, never blocks a request.
func projectForRemoteAddr(remoteAddr string) string {
	host, portStr, err := net.SplitHostPort(remoteAddr)
	if err != nil || (host != "127.0.0.1" && host != "::1" && host != "localhost") {
		return ""
	}

	projCache.mu.Lock()
	if c, ok := projCache.byAddr[remoteAddr]; ok && time.Now().Before(c.expires) {
		projCache.mu.Unlock()
		return c.dir
	}
	projCache.mu.Unlock()

	dir := lookupProjectDir(portStr)

	projCache.mu.Lock()
	projCache.byAddr[remoteAddr] = cachedProject{dir: dir, expires: time.Now().Add(projectCacheTTL)}
	projCache.mu.Unlock()
	return dir
}

func lookupProjectDir(port string) string {
	pid := lookupClientPID(port)
	if pid <= 0 {
		return ""
	}
	return lookupCwd(pid)
}

// lookupClientPID finds the PID on the other end of the connection whose
// client-side port is `port` — i.e. not this am process itself.
func lookupClientPID(port string) int {
	out, err := exec.Command("lsof", "-iTCP:"+port, "-sTCP:ESTABLISHED", "-Fp").Output()
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "p") {
			continue
		}
		pid, err := strconv.Atoi(line[1:])
		if err != nil || pid == projCache.ourPID {
			continue
		}
		return pid
	}
	return 0
}

func lookupCwd(pid int) string {
	out, err := exec.Command("lsof", "-p", strconv.Itoa(pid), "-a", "-d", "cwd", "-Fn").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "n/") {
			return line[1:]
		}
	}
	return ""
}
