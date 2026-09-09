package proxy

import (
	"errors"
	"os"
	"sync"
	"syscall"
)

// Lifecycle tracks active Claude sessions by PID for status reporting.
type Lifecycle struct {
	mu   sync.Mutex
	pids map[int]bool
}

func NewLifecycle() *Lifecycle {
	return &Lifecycle{pids: make(map[int]bool)}
}

func (l *Lifecycle) AddSession(pid int) {
	if pid <= 0 {
		// Defense in depth against the server.go handler's own guard: pid<=0
		// would never get pruned by pruneDead (kill(0, sig) signals the
		// caller's process group and always succeeds, it doesn't mean "pid 0
		// is alive"), so it would sit in the map forever.
		return
	}
	l.mu.Lock()
	l.pids[pid] = true
	l.mu.Unlock()
}

func (l *Lifecycle) EndSession(pid int) {
	l.mu.Lock()
	delete(l.pids, pid)
	l.mu.Unlock()
}

func (l *Lifecycle) Sessions() int {
	return l.pruneDead()
}

func (l *Lifecycle) pruneDead() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	for pid := range l.pids {
		proc, err := os.FindProcess(pid)
		if err != nil {
			delete(l.pids, pid)
			continue
		}
		err = proc.Signal(syscall.Signal(0))
		if err == nil {
			continue // alive and signalable
		}
		if errors.Is(err, syscall.EPERM) {
			// kill(pid, 0) returning EPERM means the kernel found a live
			// process but refused the signal because we don't own it (e.g.
			// `am proxy` running as one user, tracking a Claude Code
			// session launched as another). That's "alive, just not ours"
			// — not "dead". Treating it as dead (the previous behavior,
			// which deleted on *any* non-nil error) would silently drop a
			// live session from `am status` and could let `am proxy down
			// --force` proceed past its attached-session check while a
			// real session is still attached.
			continue
		}
		// ESRCH ("no such process") or anything else: actually gone.
		delete(l.pids, pid)
	}
	return len(l.pids)
}
