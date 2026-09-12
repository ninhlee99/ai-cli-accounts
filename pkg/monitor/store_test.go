package monitor

import "testing"

func TestTruncateRunes(t *testing.T) {
	if got := TruncateRunes("hello", 10); got != "hello" {
		t.Fatal(got)
	}
	if got := TruncateRunes("abcdefghij", 5); got != "abcde…" {
		t.Fatal(got)
	}
}

func TestAppendAndLoad(t *testing.T) {
	t.Setenv("AM_HOME", t.TempDir())
	AppendEvent("ROTATE", "a → b")
	AppendEvent("POOL", "failover gemini")
	ev := LoadEvents(10, "rotate")
	if len(ev) != 1 || ev[0].Tag != "ROTATE" {
		t.Fatalf("%+v", ev)
	}
	ev = LoadEvents(10, "failover")
	if len(ev) != 1 {
		t.Fatalf("%+v", ev)
	}
}
