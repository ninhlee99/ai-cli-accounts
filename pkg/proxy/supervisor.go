package proxy

import (
	"log"
	"net/http"
	"os/exec"
	"time"
)

// RunSupervisor is what `am proxy --supervise` runs (spawned by
// CmdProxyUp instead of the bare server directly). It keeps a real `am
// proxy` server child alive:
//
//   - clean exit (code 0), only reachable via /_am/shutdown, i.e. a
//     deliberate `am proxy down` — the supervisor exits too. This was
//     requested, not a crash, so nothing is restarted.
//   - any other exit (crash, signal-killed, OOM) — restarted with capped
//     backoff. Repeated crashes within a short window are treated as a
//     crash-loop instead of retried forever.
//   - crash-loop reached — falls back to binding addr itself with the
//     minimal Anthropic-direct passthrough handler (see
//     newPassthroughHandler, degraded=true) so already-running `claude`
//     processes keep working in degraded form instead of getting
//     connection-refused. Stays that way until `am proxy up` recovers it
//     (see proxyStatusMode/CmdProxyUp).
func RunSupervisor(addr, upstream string) error {
	bin, err := resolveAMBin()
	if err != nil {
		return err
	}

	var crashes []time.Time
	attempt := 0
	for {
		startedAt := time.Now()
		cmd := exec.Command(bin, "proxy", "--addr", addr, "--upstream", upstream)
		if err := cmd.Start(); err != nil {
			log.Printf("amux proxy supervisor: spawn failed: %v", err)
			crashes = append(crashes, time.Now())
		} else {
			waitErr := cmd.Wait()
			if waitErr == nil && cmd.ProcessState != nil && cmd.ProcessState.ExitCode() == 0 {
				log.Printf("amux proxy supervisor: server exited cleanly, stopping")
				return nil
			}
			log.Printf("amux proxy supervisor: server exited unexpectedly: %v", waitErr)
			now := time.Now()
			// A child that ran for a while before dying isn't a tight
			// crash-loop yet — give it a fresh run of backoff attempts
			// rather than let a long-past flaky crash keep the delay
			// pinned at supervisorMaxBackoff forever.
			if now.Sub(startedAt) >= supervisorStableUptime {
				attempt = 0
				crashes = nil
			}
			crashes = append(crashes, now)
		}

		if crashLooping(crashes, time.Now()) {
			break
		}
		time.Sleep(superBackoff(attempt))
		attempt++
	}

	log.Printf("amux proxy supervisor: crash-looping, falling back to degraded Anthropic-direct passthrough on %s", addr)
	rot := NewRotator("claude")
	life := NewLifecycle()
	var srv *http.Server
	handler, err := newPassthroughHandler(rot, life, upstream, true, func() {
		if srv != nil {
			_ = srv.Close()
		}
	})
	if err != nil {
		return err
	}
	srv = &http.Server{Addr: addr, Handler: handler}
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

const (
	supervisorBaseBackoff  = 500 * time.Millisecond
	supervisorMaxBackoff   = 8 * time.Second
	supervisorCrashWindow  = 60 * time.Second
	supervisorMaxCrashes   = 5
	supervisorStableUptime = 10 * time.Second
)

// superBackoff returns the delay before the (attempt+1)th respawn:
// 0.5s, 1s, 2s, 4s, 8s, 8s, ... — capped at supervisorMaxBackoff.
func superBackoff(attempt int) time.Duration {
	d := supervisorBaseBackoff
	for i := 0; i < attempt; i++ {
		d *= 2
		if d >= supervisorMaxBackoff {
			return supervisorMaxBackoff
		}
	}
	return d
}

// crashLooping reports whether history contains supervisorMaxCrashes or
// more entries within supervisorCrashWindow of now.
func crashLooping(history []time.Time, now time.Time) bool {
	count := 0
	for _, t := range history {
		if now.Sub(t) <= supervisorCrashWindow {
			count++
		}
	}
	return count >= supervisorMaxCrashes
}
