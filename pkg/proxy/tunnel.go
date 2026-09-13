package proxy

import (
	"io"
	"net"
	"net/http"
	"time"
)

// handleConnectTunnel implements standard HTTP CONNECT tunneling so clients
// that use HTTP_PROXY / HTTPS_PROXY (e.g. agy, curl, git, SDKs) can use amux
// as a forward proxy.
func handleConnectTunnel(w http.ResponseWriter, r *http.Request) {
	destConn, err := net.DialTimeout("tcp", r.Host, 10*time.Second)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		destConn.Close()
		http.Error(w, "hijacking not supported", http.StatusInternalServerError)
		return
	}

	clientConn, _, err := hijacker.Hijack()
	if err != nil {
		destConn.Close()
		return
	}

	_, _ = clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))

	go func() {
		defer destConn.Close()
		defer clientConn.Close()
		_, _ = io.Copy(destConn, clientConn)
	}()

	go func() {
		defer destConn.Close()
		defer clientConn.Close()
		_, _ = io.Copy(clientConn, destConn)
	}()
}
