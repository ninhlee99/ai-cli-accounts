package env

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrintEnvExports_ProxyUpIncludesGatewayCreds(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AM_HOME", dir)
	_ = os.WriteFile(filepath.Join(dir, "env.json"), []byte(`{"FOO":"bar"}`), 0o600)

	var buf bytes.Buffer
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	PrintEnvExports(true, true, "http://127.0.0.1:8787")
	_ = w.Close()
	os.Stdout = old
	_, _ = buf.ReadFrom(r)
	out := buf.String()

	if !strings.Contains(out, "export ANTHROPIC_BASE_URL=http://127.0.0.1:8787\n") {
		t.Fatalf("missing BASE_URL: %q", out)
	}
	if !strings.Contains(out, "export ANTHROPIC_AUTH_TOKEN=am-proxy\n") {
		t.Fatalf("missing AUTH_TOKEN: %q", out)
	}
	if !strings.Contains(out, "export FOO='bar'\n") {
		t.Fatalf("missing custom env: %q", out)
	}
}

func TestPrintEnvExports_ProxyDownOmitsAnthropic(t *testing.T) {
	t.Setenv("AM_HOME", t.TempDir())
	var buf bytes.Buffer
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	PrintEnvExports(false, false, "http://127.0.0.1:8787")
	_ = w.Close()
	os.Stdout = old
	_, _ = buf.ReadFrom(r)
	out := buf.String()
	if strings.Contains(out, "ANTHROPIC_") {
		t.Fatalf("should omit Anthropic vars when proxy down: %q", out)
	}
}
