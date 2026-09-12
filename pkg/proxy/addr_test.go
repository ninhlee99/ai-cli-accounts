package proxy

import (
	"os"
	"path/filepath"
	"testing"
)

func TestComposeListenAddr(t *testing.T) {
	if got := ComposeListenAddr("", "", true); got != "0.0.0.0:8787" {
		t.Fatalf("public default port: %q", got)
	}
	if got := ComposeListenAddr("", "8989", true); got != "0.0.0.0:8989" {
		t.Fatalf("public + port: %q", got)
	}
	if got := ComposeListenAddr("0.0.0.0", "9000", false); got != "0.0.0.0:9000" {
		t.Fatalf("addr + port: %q", got)
	}
	if got := ComposeListenAddr("0.0.0.0:7777", "8989", false); got != "0.0.0.0:8989" {
		t.Fatalf("port override: %q", got)
	}
	if got := ComposeListenAddr("", "8989", false); got != "127.0.0.1:8989" {
		t.Fatalf("port only: %q", got)
	}
}

func TestParseListenArgs(t *testing.T) {
	listen, ok, err := ParseListenArgs([]string{"--public", "-p", "8989"})
	if err != nil || !ok || listen != "0.0.0.0:8989" {
		t.Fatalf("got %q ok=%v err=%v", listen, ok, err)
	}
	listen, ok, err = ParseListenArgs([]string{"-b", "0.0.0.0", "--port", "9000"})
	if err != nil || !ok || listen != "0.0.0.0:9000" {
		t.Fatalf("got %q ok=%v err=%v", listen, ok, err)
	}
	listen, ok, err = ParseListenArgs([]string{"--addr", "192.168.1.5:8787"})
	if err != nil || !ok || listen != "192.168.1.5:8787" {
		t.Fatalf("got %q ok=%v err=%v", listen, ok, err)
	}
	_, ok, err = ParseListenArgs([]string{"--threshold", "95"})
	if ok || err != nil {
		t.Fatalf("expected no listen flags, ok=%v err=%v", ok, err)
	}
	_, ok, err = ParseListenArgs([]string{"-p"})
	if !ok || err == nil {
		t.Fatalf("expected usage error for bare -p, ok=%v err=%v", ok, err)
	}
}

func TestDialAddr_RewritesWildcard(t *testing.T) {
	if got := DialAddr("0.0.0.0:8787"); got != "127.0.0.1:8787" {
		t.Fatalf("got %q", got)
	}
	if got := DialAddr("127.0.0.1:8787"); got != "127.0.0.1:8787" {
		t.Fatalf("got %q", got)
	}
	if got := DialAddr("192.168.1.5:9000"); got != "192.168.1.5:9000" {
		t.Fatalf("got %q", got)
	}
}

func TestNormalizeListenAddr(t *testing.T) {
	if got := normalizeListenAddr(":8787"); got != "0.0.0.0:8787" {
		t.Fatalf("got %q", got)
	}
	if got := normalizeListenAddr("0.0.0.0"); got != "0.0.0.0:8787" {
		t.Fatalf("got %q", got)
	}
	if got := normalizeListenAddr(""); got != defaultListenAddr {
		t.Fatalf("got %q", got)
	}
}

func TestIsPublicListen(t *testing.T) {
	if !IsPublicListen("0.0.0.0:8787") {
		t.Fatal("expected public")
	}
	if IsPublicListen("127.0.0.1:8787") {
		t.Fatal("localhost not public")
	}
}

func TestResolveListenAddr_OverrideAndPersist(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AM_HOME", dir)
	t.Setenv("AM_PROXY_LISTEN", "")
	t.Setenv("AM_PROXY_ADDR", "")

	if got := ResolveListenAddr(""); got != defaultListenAddr {
		t.Fatalf("default got %q", got)
	}
	if got := ResolveListenAddr("0.0.0.0:8787"); got != "0.0.0.0:8787" {
		t.Fatalf("override got %q", got)
	}

	t.Setenv("AM_PROXY_LISTEN", "0.0.0.0:9999")
	if got := ResolveListenAddr(""); got != "0.0.0.0:9999" {
		t.Fatalf("env got %q", got)
	}
	t.Setenv("AM_PROXY_LISTEN", "")
	if err := SaveListenAddr("0.0.0.0:8888"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "proxy.bind.json")); err != nil {
		t.Fatal(err)
	}
	if got := ResolveListenAddr(""); got != "0.0.0.0:8888" {
		t.Fatalf("persisted got %q", got)
	}
	if !IsPublic() {
		t.Fatal("expected public bind after SaveListenAddr")
	}
}
