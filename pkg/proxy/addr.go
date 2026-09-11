package proxy

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"amux-accounts/pkg/types"
)

const defaultProxyPort = "8787"
const defaultListenAddr = "127.0.0.1:" + defaultProxyPort

// DefaultProxyPort is the port used when --port / -p is omitted.
const DefaultProxyPort = defaultProxyPort

// ComposeListenAddr builds host:port for the daemon.
// public forces host 0.0.0.0. Empty host defaults to 127.0.0.1; empty port to 8787.
// If host already contains a port and portOverride is set, portOverride wins.
func ComposeListenAddr(host, portOverride string, public bool) string {
	host = strings.TrimSpace(host)
	portOverride = strings.TrimSpace(portOverride)

	if public {
		// Keep port from host:port if present, unless --port overrides.
		if h, p, err := net.SplitHostPort(host); err == nil {
			host = h
			if portOverride == "" {
				portOverride = p
			}
		}
		host = "0.0.0.0"
	} else if host != "" {
		if h, p, err := net.SplitHostPort(host); err == nil {
			host = h
			if portOverride == "" {
				portOverride = p
			}
		}
	}

	if host == "" {
		host = "127.0.0.1"
	}
	if portOverride == "" {
		portOverride = defaultProxyPort
	}
	return net.JoinHostPort(host, portOverride)
}

// ParseListenArgs reads --public, --addr/-b, --port/-p from argv.
// ok is false when none of those flags appear (caller keeps env/persisted/default).
func ParseListenArgs(args []string) (listen string, ok bool, err error) {
	var host, port string
	public := false
	seen := false

	takeVal := func(flag string, i *int) (string, error) {
		if *i+1 >= len(args) || strings.HasPrefix(args[*i+1], "-") {
			return "", fmt.Errorf("usage: %s <value>", flag)
		}
		*i++
		return args[*i], nil
	}

	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--public":
			public = true
			seen = true
		case "--addr", "-b":
			v, e := takeVal(a, &i)
			if e != nil {
				return "", true, e
			}
			host = v
			seen = true
		case "--port", "-p":
			v, e := takeVal(a, &i)
			if e != nil {
				return "", true, e
			}
			port = v
			seen = true
		default:
			if strings.HasPrefix(a, "--addr=") {
				host = strings.TrimPrefix(a, "--addr=")
				seen = true
			} else if strings.HasPrefix(a, "--port=") {
				port = strings.TrimPrefix(a, "--port=")
				seen = true
			}
		}
	}
	if !seen {
		return "", false, nil
	}
	return ComposeListenAddr(host, port, public), true, nil
}

// ListenAddrPath is deprecated legacy path; persistence lives in proxy.bind.json.
// Kept for tests that historically wrote proxy-listen.
func ListenAddrPath() string {
	return filepath.Join(types.BaseDir(), "proxy-listen")
}

// SaveListenAddr remembers where the daemon should bind by updating
// proxy.bind.json (public + port). Also mirrors to legacy proxy-listen.
func SaveListenAddr(addr string) error {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return nil
	}
	if err := SaveBindListen(addr); err != nil {
		return err
	}
	_ = os.MkdirAll(types.BaseDir(), 0o700)
	_ = os.WriteFile(ListenAddrPath(), []byte(addr+"\n"), 0o600)
	return nil
}

// LoadListenAddr returns the persisted bind address from bind config, or "".
func LoadListenAddr() string {
	c := loadBindConfig()
	if c.Port == "" && !c.Public {
		// legacy file fallback
		b, err := os.ReadFile(ListenAddrPath())
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(b))
	}
	return ListenAddr()
}

// ResolveListenAddr picks the bind address for the proxy daemon.
// Order: explicit override → AM_PROXY_LISTEN → bind config / legacy → AM_PROXY_ADDR → localhost.
func ResolveListenAddr(override string) string {
	if a := strings.TrimSpace(override); a != "" {
		return normalizeListenAddr(a)
	}
	if a := strings.TrimSpace(os.Getenv("AM_PROXY_LISTEN")); a != "" {
		return normalizeListenAddr(a)
	}
	// Prefer bind.json (main) when configured.
	c := loadBindConfig()
	if c.Public || c.Port != "" {
		return ListenAddr()
	}
	if a := LoadListenAddr(); a != "" {
		return normalizeListenAddr(a)
	}
	if a := strings.TrimSpace(os.Getenv("AM_PROXY_ADDR")); a != "" {
		// AM_PROXY_ADDR is the dial address; only use as listen if not loopback-only default path
		return normalizeListenAddr(a)
	}
	return ListenAddr()
}

// normalizeListenAddr accepts host, host:port, or :port.
func normalizeListenAddr(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return defaultListenAddr
	}
	if strings.HasPrefix(addr, ":") {
		return "0.0.0.0" + addr
	}
	if _, _, err := net.SplitHostPort(addr); err == nil {
		return addr
	}
	// bare host → default port
	return net.JoinHostPort(addr, defaultProxyPort)
}

// DialAddr is where local `am` talks to the proxy. Binding 0.0.0.0 is not a
// connectable host, so we rewrite it (and ::) to 127.0.0.1 for loopback.
func DialAddr(listen string) string {
	listen = normalizeListenAddr(listen)
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "127.0.0.1:" + defaultProxyPort
	}
	switch host {
	case "0.0.0.0", "::", "[::]", "":
		return net.JoinHostPort("127.0.0.1", port)
	default:
		return net.JoinHostPort(host, port)
	}
}

// IsPublicListen reports whether the bind address accepts remote clients.
func IsPublicListen(listen string) bool {
	host, _, err := net.SplitHostPort(normalizeListenAddr(listen))
	if err != nil {
		return false
	}
	return host == "0.0.0.0" || host == "::" || host == "[::]"
}

// PrimaryLANIP returns a best-effort non-loopback IPv4 for status / am env.
func PrimaryLANIP() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		if ip := firstIPv4(iface); ip != "" {
			return ip
		}
	}
	return ""
}

func firstIPv4(iface net.Interface) string {
	addrs, err := iface.Addrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip == nil || ip.IsLoopback() {
			continue
		}
		ip = ip.To4()
		if ip != nil {
			return ip.String()
		}
	}
	return ""
}

// PublicBaseURL builds http://<lan-ip>:<port> when listening publicly.
func PublicBaseURL(listen string) string {
	if !IsPublicListen(listen) {
		return ""
	}
	_, port, err := net.SplitHostPort(normalizeListenAddr(listen))
	if err != nil {
		port = defaultProxyPort
	}
	ip := PrimaryLANIP()
	if ip == "" {
		return fmt.Sprintf("http://<your-lan-ip>:%s", port)
	}
	return "http://" + net.JoinHostPort(ip, port)
}
