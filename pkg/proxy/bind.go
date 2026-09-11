package proxy

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"amux-accounts/pkg/types"
)

type bindConfig struct {
	Public bool   `json:"public"`
	Port   string `json:"port,omitempty"`
}

func bindConfigPath() string {
	return filepath.Join(types.BaseDir(), "proxy.bind.json")
}

func loadBindConfig() bindConfig {
	b, err := os.ReadFile(bindConfigPath())
	if err != nil {
		return bindConfig{}
	}
	var c bindConfig
	if json.Unmarshal(b, &c) != nil {
		return bindConfig{}
	}
	return c
}

func saveBindConfig(c bindConfig) error {
	_ = os.MkdirAll(types.BaseDir(), 0o700)
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(bindConfigPath(), b, 0o600)
}

// SaveBindPublic persists whether the daemon should listen on 0.0.0.0.
func SaveBindPublic(public bool) error {
	c := loadBindConfig()
	c.Public = public
	if c.Port == "" {
		c.Port = listenPort()
	}
	return saveBindConfig(c)
}

// SaveBindPort persists the daemon listen port (keeps public flag).
func SaveBindPort(port string) error {
	port = strings.TrimSpace(port)
	if port == "" {
		return nil
	}
	c := loadBindConfig()
	c.Port = port
	return saveBindConfig(c)
}

// SaveBindListen derives public/port from a full listen addr (host:port) and
// persists them in proxy.bind.json — single source of truth for ListenAddr().
func SaveBindListen(addr string) error {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return nil
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		// bare host or :port — normalize via addr helpers when available
		addr = normalizeListenAddr(addr)
		host, port, err = net.SplitHostPort(addr)
		if err != nil {
			return fmt.Errorf("invalid listen addr %q: %w", addr, err)
		}
	}
	c := loadBindConfig()
	c.Public = host == "0.0.0.0" || host == "::" || host == ""
	if port != "" {
		c.Port = port
	}
	return saveBindConfig(c)
}

// IsPublic reports saved public-bind preference.
func IsPublic() bool { return loadBindConfig().Public }

// listenPort extracts the port from AM_PROXY_ADDR / default client addr.
func listenPort() string {
	c := loadBindConfig()
	if c.Port != "" {
		return c.Port
	}
	host := os.Getenv("AM_PROXY_ADDR")
	if host == "" {
		host = "127.0.0.1:8787"
	}
	_, port, err := net.SplitHostPort(host)
	if err != nil || port == "" {
		return "8787"
	}
	return port
}

// ProxyAddr is the address local clients use for health checks / base URL
// (always loopback — even when the daemon binds 0.0.0.0).
func ProxyAddr() string {
	if addr := os.Getenv("AM_PROXY_ADDR"); addr != "" {
		return addr
	}
	return "127.0.0.1:" + listenPort()
}

func proxyAddr() string { return ProxyAddr() }

// ListenAddr is the bind address for the daemon process.
func ListenAddr() string {
	port := listenPort()
	if IsPublic() {
		return "0.0.0.0:" + port
	}
	return "127.0.0.1:" + port
}

// IsPublicBind reports whether a listen addr is public (0.0.0.0 / ::).
func IsPublicBind(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	host = strings.Trim(host, "[]")
	return host == "0.0.0.0" || host == "::" || host == ""
}

// LocalIPv4s returns non-loopback IPv4 addresses for public URL display.
func LocalIPv4s() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
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
			if ip == nil {
				continue
			}
			s := ip.String()
			if seen[s] {
				continue
			}
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// PublicURLs returns http://<lan-ip>:<port> for each local IPv4.
func PublicURLs() []string {
	port := listenPort()
	ips := LocalIPv4s()
	urls := make([]string, 0, len(ips))
	for _, ip := range ips {
		urls = append(urls, fmt.Sprintf("http://%s:%s", ip, port))
	}
	return urls
}

// FormatPublicHosts joins public URLs for status display.
func FormatPublicHosts() string {
	urls := PublicURLs()
	if len(urls) == 0 {
		return "(no LAN IP detected)"
	}
	return strings.Join(urls, "  ")
}
