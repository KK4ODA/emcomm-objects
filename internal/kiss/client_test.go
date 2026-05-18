package kiss

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"
)

// TestClient_SendAndReconnect spins up a tiny TCP server that accepts a
// connection, reads one KISS frame, closes; then accepts a second connection
// and reads a second frame. The client must reconnect between them.
func TestClient_SendAndReconnect(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	type received struct {
		frame *Frame
		err   error
	}
	got := make(chan received, 2)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 2; i++ {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			r := NewReader(conn)
			f, err := r.ReadFrame()
			got <- received{f, err}
			conn.Close()
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := NewClient(ln.Addr().String(), 0, nil, nil)
	client.reconnect = ReconnectPolicy{Initial: 10 * time.Millisecond, Max: 50 * time.Millisecond}

	clientDone := make(chan struct{})
	go func() {
		client.Run(ctx)
		close(clientDone)
	}()

	// Wait for first connect.
	if !waitForState(client, StateConnected, 2*time.Second) {
		t.Fatal("client never connected (1st)")
	}

	if err := client.SendAX25([]byte{0xAA, 0xBB, 0xCC}); err != nil {
		t.Fatalf("first send: %v", err)
	}

	// Server closes after reading; client should reconnect.
	if !waitForState(client, StateConnected, 2*time.Second) {
		t.Fatal("client never reconnected")
	}

	if err := client.SendAX25([]byte{0x11, 0x22}); err != nil {
		t.Fatalf("second send: %v", err)
	}

	cancel()
	<-clientDone
	wg.Wait()
	close(got)

	var frames []*Frame
	for r := range got {
		if r.err != nil {
			t.Fatalf("server read: %v", r.err)
		}
		frames = append(frames, r.frame)
	}
	if len(frames) != 2 {
		t.Fatalf("got %d frames", len(frames))
	}
	if string(frames[0].Payload) != string([]byte{0xAA, 0xBB, 0xCC}) {
		t.Errorf("first payload: %x", frames[0].Payload)
	}
	if string(frames[1].Payload) != string([]byte{0x11, 0x22}) {
		t.Errorf("second payload: %x", frames[1].Payload)
	}
}

func TestClient_SendWhenDisconnected(t *testing.T) {
	c := NewClient("127.0.0.1:1", 0, nil, nil) // port 1, won't connect
	if err := c.SendAX25([]byte{0x01}); err != ErrNotConnected {
		t.Errorf("got %v, want ErrNotConnected", err)
	}
}

func waitForState(c *Client, want State, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		// Need to also detect the brief "between" state when the server has
		// closed and the client hasn't yet flipped to Disconnected. We wait
		// until we observe State == want for two consecutive samples.
		if c.State() == want {
			time.Sleep(20 * time.Millisecond)
			if c.State() == want {
				return true
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}
