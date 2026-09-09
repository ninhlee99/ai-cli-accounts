package proxy

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// newPassthroughHandler serves a minimal subset of the full proxy: every
// non-admin request is forwarded straight to the real Anthropic API via the
// same reverse-proxy logic normal operation already uses (see
// newReverseProxy) — this is the "talk to Anthropic directly" safety net,
// used two ways:
//
//   - degraded=true: RunSupervisor's crash-loop fallback. The full server
//     keeps failing to start, so this binds the port itself and stays up
//     indefinitely so already-running `claude` processes don't get
//     connection-refused.
//   - degraded=false: server.go's brief grace-drain window between an
//     intentional `am proxy down` and the socket actually closing, so
//     in-flight/near-term requests still get a real answer instead of
//     being cut off mid-session.
//
// It deliberately does not serve /_am/pool, /_am/switch-provider, or the
// OpenAI bridge routes (/v1/chat/completions etc.) — those depend on
// pkg/provider/pkg/bridge, exactly the kind of subsystem that might be why
// the full server crashed in the first place, so this mode stays minimal
// and self-contained (Rotator + net/http/httputil only).
func newPassthroughHandler(rot *Rotator, life *Lifecycle, upstream string, degraded bool, shutdown func()) (http.Handler, error) {
	rp, err := newReverseProxy(upstream, rot)
	if err != nil {
		return nil, err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/_am/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		s := rot.Status()
		s["sessions"] = life.Sessions()
		s["upstream"] = upstream
		if degraded {
			s["mode"] = "degraded"
		} else {
			s["mode"] = "draining"
		}
		_ = json.NewEncoder(w).Encode(s)
	})
	mux.HandleFunc("/_am/session", func(w http.ResponseWriter, r *http.Request) {
		// Mirrors the admin endpoint in server.go's newHandler — see the
		// comment there for why pid<=0 is rejected outright.
		pid, err := strconv.Atoi(r.URL.Query().Get("pid"))
		if err != nil || pid <= 0 {
			http.Error(w, "invalid or missing 'pid'", http.StatusBadRequest)
			return
		}
		event := r.URL.Query().Get("event")
		if event == "" {
			event = r.URL.Query().Get("op")
		}
		switch event {
		case "start":
			life.AddSession(pid)
		case "end":
			life.EndSession(pid)
		}
		fmt.Fprintf(w, "%d\n", life.Sessions())
	})
	if shutdown != nil {
		mux.HandleFunc("/_am/shutdown", func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintln(w, "ok")
			go func() {
				time.Sleep(100 * time.Millisecond)
				shutdown()
			}()
		})
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/_am/") {
			mux.ServeHTTP(w, r)
			return
		}
		rp.ServeHTTP(w, r)
	}), nil
}
