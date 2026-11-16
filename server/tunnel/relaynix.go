package tunnel

import (
	"context"
	"io"
	"net"
	"time"
)

// The relay's side of Nix services: it passes the server's set and
// actions to the Nix agent, and the agent's reports to the server. It
// builds and runs nothing itself.

// nixLoop sends the server what the agent reports, while this session
// lasts. What it can't send (the link dropped) goes back to the agent
// client for the next session, so a flake.lock a relay made isn't lost.
func (r *Relay) nixLoop(done <-chan struct{}, send func(Control) error, hello func() error) {
	for {
		select {
		case <-done:
			return
		case <-r.Nix.Notify():
		}
		if left, ok := sendReport(r.Nix.Take(), send, hello); !ok {
			r.Nix.Requeue(left)
			return
		}
	}
}

// sendReport sends rep in order; on a failure it returns what's left.
// (A new session says hello anyway, so HostChanged isn't kept.)
func sendReport(rep NixReport, send func(Control) error, hello func() error) (NixReport, bool) {
	rest := func(status []map[string]NixStatus, locks []NixLock) NixReport {
		return NixReport{Status: status, Locks: locks, Known: rep.Known, HasKnown: rep.HasKnown}
	}
	if rep.HostChanged && hello() != nil {
		return rest(rep.Status, rep.Locks), false
	}
	for i, st := range rep.Status {
		if send(Control{Type: "nixStatus", NixStatus: st}) != nil {
			return rest(rep.Status[i:], rep.Locks), false
		}
	}
	for i := range rep.Locks {
		if send(Control{Type: "nixLock", NixLock: &rep.Locks[i]}) != nil {
			return rest(nil, rep.Locks[i:]), false
		}
	}
	if rep.HasKnown && send(Control{Type: "nixKnown", NixKnown: rep.Known}) != nil {
		return rest(nil, nil), false
	}
	return NixReport{}, true
}

// serveNixLogs answers a nixLogs stream with the agent's logs.
func (r *Relay) serveNixLogs(st net.Conn, h StreamHeader) {
	defer st.Close()
	if r.Nix == nil || r.Nix.Host() == nil {
		_ = writeLine(st, StreamReply{Error: "there's no Nix agent on this relay's machine"})
		return
	}
	req := NixLogs{}
	if h.Logs != nil {
		req = *h.Logs
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rc, err := r.Nix.Logs(ctx, h.NixLogs, req)
	if err != nil {
		_ = writeLine(st, StreamReply{Error: err.Error()})
		return
	}
	defer rc.Close()
	if err := writeLine(st, StreamReply{}); err != nil {
		return
	}
	_ = st.SetDeadline(time.Time{})
	// The server closing the stream (the viewer left) ends the logs.
	go func() {
		_, _ = io.Copy(io.Discard, st)
		cancel()
		rc.Close()
	}()
	_, _ = io.Copy(st, rc)
}
