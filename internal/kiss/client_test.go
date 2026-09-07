package kiss

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/kk4oda/emcomm-objects/internal/ax25"
	"github.com/kk4oda/emcomm-objects/internal/link"
)

// fakeTNC accepts one connection and returns the KISS frames it reads.
func fakeTNC(t *testing.T) (addr string, frames <-chan *Frame) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	ch := make(chan *Frame, 8)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		r := NewReader(conn)
		for {
			f, err := r.ReadFrame()
			if err != nil {
				return
			}
			ch <- f
		}
	}()
	return ln.Addr().String(), ch
}

func waitState(t *testing.T, c *Client, want link.Status) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if c.State().Status == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("state %s not reached (now %s)", want, c.State().StatusStr)
}

func TestClientSendsKISSFrame(t *testing.T) {
	addr, frames := fakeTNC(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var states []link.State
	c := NewClient(addr, 0, nil, func(s link.State) { states = append(states, s) })
	go c.Run(ctx)

	// Disabled: must not connect and Send must refuse.
	time.Sleep(50 * time.Millisecond)
	if c.State().Status != link.Disconnected {
		t.Fatal("client dialed while disabled")
	}
	if err := c.SendAX25([]byte{1}); err != link.ErrNotConnected {
		t.Fatalf("expected ErrNotConnected, got %v", err)
	}

	c.SetEnabled(true)
	waitState(t, c, link.Connected)

	src, _ := ax25.ParseAddress("N0CALL-12")
	dst, _ := ax25.ParseAddress("APZEMC")
	p := link.Packet{Source: src, Dest: dst, Info: ";TEST     *092000z3348.00N/08418.00Wr"}
	if err := c.Send(ctx, p); err != nil {
		t.Fatal(err)
	}
	select {
	case f := <-frames:
		if f.Port != 0 || f.Command != CmdData {
			t.Fatalf("unexpected KISS header %+v", f)
		}
		want, _ := ax25.EncodeUI(dst, src, nil, []byte(p.Info))
		if string(f.Payload) != string(want) {
			t.Fatalf("payload mismatch")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no frame received")
	}

	c.SetEnabled(false)
	waitState(t, c, link.Disconnected)
	if len(states) == 0 || states[len(states)-1].Detail != "inactive" {
		t.Fatalf("state callbacks: %+v", states)
	}
}

func TestClientReconnectsAfterUnreachable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := NewClient("127.0.0.1:1", 0, nil, nil) // nothing listens on port 1
	c.reconnect = ReconnectPolicy{Initial: 20 * time.Millisecond, Max: 40 * time.Millisecond}
	go c.Run(ctx)
	c.SetEnabled(true)
	time.Sleep(80 * time.Millisecond)
	if c.State().Status == link.Connected {
		t.Fatal("cannot be connected to a closed port")
	}
	addr, _ := fakeTNC(t)
	c.Reconfigure(addr, 0)
	waitState(t, c, link.Connected)
}
