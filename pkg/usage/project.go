package usage

import (
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

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

// ProjectForRemoteAddr resolves a client connection's cwd from RemoteAddr.
func ProjectForRemoteAddr(remoteAddr string) string {
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
	dir := lookupCwd(pid)
	if dir == "" {
		return dir
	}
	return gitRootOrSelf(dir)
}

// gitRootOrSelf resolves dir to its enclosing git repository root, so usage
// run from a subdirectory of a repo (rather than the repo's own top level)
// is still attributed to the repo instead of that subfolder's name — e.g.
// ProjectLabel (usage.go) does filepath.Base(dir), and a raw client cwd of
// ".../amux/pkg/usage" would otherwise label usage "usage" instead of
// "amux". Falls back to dir unchanged when it isn't inside a git repo (or
// git isn't on PATH); the caller's own cache (projCache, projectCacheTTL)
// already covers this from running on every proxied request.
func gitRootOrSelf(dir string) string {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return dir
	}
	root := strings.TrimSpace(string(out))
	if root == "" {
		return dir
	}
	return root
}

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
