package kiss

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"
)

// Client is a KISS-over-TCP client with auto-reconnect.
// Send-only from our perspective: we drain inbound frames to avoid TCP
// backpressure but ignore the contents (Graywolf shows received traffic
// in its own UI).
type Client struct {
	addr       string
	port       byte
	log        *slog.Logger
	dialer     net.Dialer
	reconnect  ReconnectPolicy
	onState    func(State)

	mu    sync.Mutex
	conn  net.Conn
	state State
}

// State of the connection, surfaced to callers for status display.
type State int

const (
	StateDisconnected State = iota
	StateConnecting
	StateConnected
)

func (s State) String() string {
	switch s {
	case StateDisconnected:
		return "disconnected"
	case StateConnecting:
		return "connecting"
	case StateConnected:
		return "connected"
	}
	return "unknown"
}

// ReconnectPolicy controls auto-reconnect backoff.
type ReconnectPolicy struct {
	Initial time.Duration
	Max     time.Duration
}

// DefaultReconnect: 1s → 30s with exponential backoff.
var DefaultReconnect = ReconnectPolicy{Initial: time.Second, Max: 30 * time.Second}

// NewClient constructs a client. addr is "host:port". kissPort is the KISS
// port byte (usually 0). onState may be nil.
func NewClient(addr string, kissPort byte, log *slog.Logger, onState func(State)) *Client {
	if log == nil {
		log = slog.Default()
	}
	if onState == nil {
		onState = func(State) {}
	}
	return &Client{
		addr:      addr,
		port:      kissPort,
		log:       log,
		dialer:    net.Dialer{Timeout: 5 * time.Second},
		reconnect: DefaultReconnect,
		onState:   onState,
	}
}

// Run dials and stays connected until ctx is cancelled, reconnecting on drop
// with exponential backoff. Intended to run in its own goroutine.
func (c *Client) Run(ctx context.Context) {
	backoff := c.reconnect.Initial
	for {
		if ctx.Err() != nil {
			return
		}
		c.setState(StateConnecting)
		conn, err := c.dialer.DialContext(ctx, "tcp", c.addr)
		if err != nil {
			c.setState(StateDisconnected)
			c.log.Warn("kiss dial failed", "addr", c.addr, "err", err, "retry_in", backoff)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = nextBackoff(backoff, c.reconnect.Max)
			continue
		}
		c.log.Info("kiss connected", "addr", c.addr)
		c.mu.Lock()
		c.conn = conn
		c.mu.Unlock()
		c.setState(StateConnected)
		backoff = c.reconnect.Initial

		// Drain inbound until the connection closes or ctx is cancelled.
		c.drainUntilClose(ctx, conn)

		c.mu.Lock()
		c.conn = nil
		c.mu.Unlock()
		c.setState(StateDisconnected)
		_ = conn.Close()
		c.log.Info("kiss disconnected")
	}
}

// SendAX25 frames an AX.25 frame as a KISS data frame and writes it. Returns
// ErrNotConnected if the client isn't currently connected.
func (c *Client) SendAX25(ax25Frame []byte) error {
	frame, err := EncodeDataFrame(c.port, ax25Frame)
	if err != nil {
		return fmt.Errorf("kiss encode: %w", err)
	}
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return ErrNotConnected
	}
	if err := conn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return fmt.Errorf("kiss set deadline: %w", err)
	}
	if _, err := conn.Write(frame); err != nil {
		return fmt.Errorf("kiss write: %w", err)
	}
	return nil
}

// State returns the current connection state.
func (c *Client) State() State {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// ErrNotConnected is returned by SendAX25 when there is no active connection.
var ErrNotConnected = errors.New("kiss: not connected")

func (c *Client) setState(s State) {
	c.mu.Lock()
	changed := c.state != s
	c.state = s
	c.mu.Unlock()
	if changed {
		c.onState(s)
	}
}

func (c *Client) drainUntilClose(ctx context.Context, conn net.Conn) {
	r := NewReader(conn)
	// Run a goroutine to close the conn on ctx cancel so ReadFrame unblocks.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()
	for {
		if _, err := r.ReadFrame(); err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) && ctx.Err() == nil {
				// Network or protocol error; drop the connection so Run reconnects.
				c.log.Debug("kiss read error, dropping connection", "err", err)
			}
			return
		}
		// Frame received from Graywolf — ignore content, we're send-only.
	}
}

func nextBackoff(d, max time.Duration) time.Duration {
	d *= 2
	if d > max {
		d = max
	}
	return d
}
