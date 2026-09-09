package provider

import (
	"net/http"
	"time"
)

// defaultHTTPClient is the fallback client used by every adapter's client()
// method when no per-adapter *http.Client override is configured. Sharing
// one client (and therefore one *http.Transport) across all requests lets
// Go's connection pool reuse TCP/TLS connections to the same upstream host
// instead of paying a fresh handshake on every single chat request, which is
// what happened when each adapter allocated `&http.Client{}` inline. The
// transport is a tuned clone of http.DefaultTransport rather than the
// package-global DefaultTransport itself, so this file doesn't reach into
// and mutate shared process-wide state.
var defaultHTTPClient = &http.Client{
	Timeout:   120 * time.Second,
	Transport: newDefaultTransport(),
}

func newDefaultTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConns = 100
	t.MaxIdleConnsPerHost = 10
	t.IdleConnTimeout = 90 * time.Second
	return t
}
