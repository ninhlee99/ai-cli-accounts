package monitor

import "amux-accounts/pkg/term"

// EnableTermSink wires term.Log → events.log so `amux watch` Logs tab
// sees proxy/CLI realtime events.
func EnableTermSink() {
	term.SetEventSink(func(tag, msg string) {
		AppendEvent(tag, msg)
	})
}
