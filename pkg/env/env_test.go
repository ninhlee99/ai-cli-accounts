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

func TestPrintEnvExports_ProxyDownUnsetsAnthropic(t *testing.T) {
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
	// A shell that ran `eval "$(am env)"` while the proxy was up has these
	// exported live — omitting the line (old behavior) left them stale.
	// Must unset explicitly so the shell falls through to api.anthropic.com.
	if !strings.Contains(out, "unset ANTHROPIC_BASE_URL\n") {
		t.Fatalf("missing unset BASE_URL: %q", out)
	}
	if !strings.Contains(out, "unset ANTHROPIC_AUTH_TOKEN\n") {
		t.Fatalf("missing unset AUTH_TOKEN: %q", out)
	}
	if strings.Contains(out, "export ANTHROPIC_") {
		t.Fatalf("should not export Anthropic vars when proxy down: %q", out)
	}
}

func TestPrintEnvExports_ProxyDownRespectsUserOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AM_HOME", dir)
	_ = os.WriteFile(filepath.Join(dir, "env.json"), []byte(`{"ANTHROPIC_BASE_URL":"https://my-gateway.example.com"}`), 0o600)

	var buf bytes.Buffer
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	PrintEnvExports(false, true, "http://127.0.0.1:8787")
	_ = w.Close()
	os.Stdout = old
	_, _ = buf.ReadFrom(r)
	out := buf.String()

	if strings.Contains(out, "unset ANTHROPIC_BASE_URL\n") {
		t.Fatalf("should not unset a user-configured override: %q", out)
	}
	if !strings.Contains(out, "export ANTHROPIC_BASE_URL='https://my-gateway.example.com'\n") {
		t.Fatalf("missing user override export: %q", out)
	}
	if !strings.Contains(out, "unset ANTHROPIC_AUTH_TOKEN\n") {
		t.Fatalf("missing unset AUTH_TOKEN: %q", out)
	}
}
