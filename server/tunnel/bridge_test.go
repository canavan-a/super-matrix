package tunnel

import (
	"bytes"
	"context"
	"net"
	"testing"
	"time"
)

func TestFrameRoundTrip(t *testing.T) {
	var b bytes.Buffer
	for i, p := range [][]byte{{}, []byte("hi"), bytes.Repeat([]byte("x"), maxDatagram)} {
		if err := writeFrame(&b, uint32(i+7), p); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeFrame(&b, 1, make([]byte, maxDatagram+1)); err != errFrameTooBig {
		t.Fatalf("oversized frame: %v", err)
	}
	buf := make([]byte, maxDatagram)
	for i, want := range []int{0, 2, maxDatagram} {
		id, p, err := readFrame(&b, buf)
		if err != nil || id != uint32(i+7) || len(p) != want {
			t.Fatalf("frame %d: id %d len %d err %v", i, id, len(p), err)
		}
	}
}

// TestUDPBridgeIdle runs both ends of a UDP bridge in-process (the stream
// is a net.Pipe) and checks that a quiet sender's flow is dropped on both
// sides, and that its next packet still gets through.
func TestUDPBridgeIdle(t *testing.T) {
	echo, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		buf := make([]byte, 2048)
		for {
			n, from, err := echo.ReadFromUDP(buf)
			if err != nil {
				return
			}
			_, _ = echo.WriteToUDP(buf[:n], from)
		}
	}()
	target := Target{ID: "b", Protocol: "udp", Host: "127.0.0.1", Port: echo.LocalAddr().(*net.UDPAddr).Port, IdleS: 1}
	open := func(context.Context) (net.Conn, error) {
		near, far := net.Pipe()
		go serveUDPTarget(far, target)
		return near, nil
	}
	bl, err := ListenBridge(Listener{ID: "b", Protocol: "udp", Host: "127.0.0.1", IdleS: 1}, open, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer bl.Close()

	c, err := net.DialUDP("udp", nil, bl.Addr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	round := func(msg string) {
		t.Helper()
		_ = c.SetDeadline(time.Now().Add(2 * time.Second))
		if _, err := c.Write([]byte(msg)); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 64)
		n, err := c.Read(buf)
		if err != nil || string(buf[:n]) != msg {
			t.Fatalf("%q: got %q %v", msg, buf[:n], err)
		}
	}
	flows := func() int {
		bl.udp.mu.Lock()
		defer bl.udp.mu.Unlock()
		return len(bl.udp.byID)
	}
	round("one")
	if n := flows(); n != 1 {
		t.Fatalf("%d flows, want 1", n)
	}
	deadline := time.Now().Add(3 * time.Second)
	for flows() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("idle flow was never dropped")
		}
		time.Sleep(50 * time.Millisecond)
	}
	round("two")
}
