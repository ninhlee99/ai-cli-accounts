package proxy

import (
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
		if err := proc.Signal(syscall.Signal(0)); err != nil {
			delete(l.pids, pid)
		}
	}
	return len(l.pids)
}
