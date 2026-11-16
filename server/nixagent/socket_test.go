package nixagent

import (
	"testing"

	"main/tunnel"
)

func TestRequeueKeepsOrderAndLocks(t *testing.T) {
	c := NewClient("/nonexistent", func(string, ...any) {})
	st := func(phase string) map[string]tunnel.NixStatus {
		return map[string]tunnel.NixStatus{"a": {Phase: phase}}
	}
	c.pending = tunnel.NixReport{Status: []map[string]tunnel.NixStatus{st("building"), st("running")}, Locks: []tunnel.NixLock{{ID: "a", Lock: "L1"}}}
	rep := c.Take()
	// The server link drops after the first status went out; meanwhile
	// the agent reported more.
	c.pending = tunnel.NixReport{Status: []map[string]tunnel.NixStatus{st("crashed")}, Known: []string{"a"}, HasKnown: true}
	c.Requeue(tunnel.NixReport{Status: rep.Status[1:], Locks: rep.Locks, HostChanged: true})
	got := c.Take()
	if len(got.Status) != 2 || got.Status[0]["a"].Phase != "running" || got.Status[1]["a"].Phase != "crashed" {
		t.Fatalf("status order: %+v", got.Status)
	}
	if len(got.Locks) != 1 || got.Locks[0].Lock != "L1" || !got.HasKnown || !got.HostChanged {
		t.Fatalf("report: %+v", got)
	}
	select {
	case <-c.Notify():
	default:
		t.Fatal("requeueing should say there's something to take")
	}
}
