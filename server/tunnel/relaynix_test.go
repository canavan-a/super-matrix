package tunnel

import (
	"errors"
	"testing"
)

func TestSendReportReturnsWhatsLeft(t *testing.T) {
	st := func(p string) map[string]NixStatus { return map[string]NixStatus{"a": {Phase: p}} }
	rep := NixReport{Status: []map[string]NixStatus{st("building"), st("running")}, Locks: []NixLock{{ID: "a", Lock: "L"}},
		Known: []string{"a"}, HasKnown: true}
	sent := 0
	left, ok := sendReport(rep, func(Control) error {
		sent++
		if sent == 2 {
			return errors.New("link dropped")
		}
		return nil
	}, func() error { return nil })
	if ok || len(left.Status) != 1 || left.Status[0]["a"].Phase != "running" || len(left.Locks) != 1 || !left.HasKnown {
		t.Fatalf("left %+v ok %v", left, ok)
	}
	// Everything goes through: nothing's left.
	if left, ok := sendReport(rep, func(Control) error { return nil }, func() error { return nil }); !ok || len(left.Status)+len(left.Locks) != 0 {
		t.Fatalf("all sent: %+v %v", left, ok)
	}
}
