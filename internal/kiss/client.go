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

	"github.com/kk4oda/emcomm-objects/internal/ax25"
	"github.com/kk4oda/emcomm-objects/internal/link"
)

// Client is a KISS-over-TCP client with auto-reconnect that implements
// link.Link. Send-only from our perspective: inbound frames are drained to
// avoid TCP backpressure and otherwise ignored (Graywolf shows received
// traffic in its own UI).
type Client struct {
	log       *slog.Logger
	dialer    net.Dialer
	reconnect ReconnectPolicy
	onState   func(link.State)
	wake      chan struct{}

	mu      sync.Mutex
	addr    string
	port    byte
	enabled bool
	conn    net.Conn
	state   link.State
}

// ReconnectPolicy controls auto-reconnect backoff.
type ReconnectPolicy struct {
	Initial time.Duration
	Max     time.Duration
}

// DefaultReconnect: 1s → 30s with exponential backoff.
var DefaultReconnect = ReconnectPolicy{Initial: time.Second, Max: 30 * time.Second}

// NewClient constructs a client for addr ("host:port") and KISS port byte
// kissPort (usually 0). onState may be nil. The client starts disabled;
// call SetEnabled(true) to make Run dial.
func NewClient(addr string, kissPort byte, log *slog.Logger, onState func(link.State)) *Client {
	if log == nil {
		log = slog.Default()
	}
	if onState == nil {
		onState = func(link.State) {}
	}
	return &Client{
		addr:      addr,
		port:      kissPort,
		log:       log,
		dialer:    net.Dialer{Timeout: 5 * time.Second},
		reconnect: DefaultReconnect,
		onState:   onState,
		wake:      make(chan struct{}, 1),
		state:     link.State{Transport: "kiss", Detail: "inactive"}.WithStatus(link.Disconnected),
	}
}

// Name implements link.Link.
func (c *Client) Name() string { return "kiss" }

// State implements link.Link.
func (c *Client) State() link.State {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// SetEnabled makes Run dial (true) or hang up and idle (false).
func (c *Client) SetEnabled(on bool) {
	c.mu.Lock()
	c.enabled = on
	conn := c.conn
	c.mu.Unlock()
	if !on && conn != nil {
		_ = conn.Close() // drainUntilClose returns, Run idles
	}
	c.kick()
}

// Reconfigure changes the endpoint; an open connection is dropped so Run
// redials the new address.
func (c *Client) Reconfigure(addr string, kissPort byte) {
	c.mu.Lock()
	changed := addr != c.addr || kissPort != c.port
	c.addr, c.port = addr, kissPort
	conn := c.conn
	c.mu.Unlock()
	if changed && conn != nil {
		_ = conn.Close()
	}
	c.kick()
}

func (c *Client) kick() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// Run dials and stays connected until ctx is cancelled, reconnecting on
// drop with exponential backoff. Intended to run in its own goroutine.
func (c *Client) Run(ctx context.Context) {
	backoff := c.reconnect.Initial
	for ctx.Err() == nil {
		c.mu.Lock()
		enabled, addr := c.enabled, c.addr
		c.mu.Unlock()
		if !enabled {
			c.setState(link.Disconnected, "inactive")
			select {
			case <-ctx.Done():
				return
			case <-c.wake:
			}
			continue
		}
		c.setState(link.Connecting, "connecting to "+addr)
		conn, err := c.dialer.DialContext(ctx, "tcp", addr)
		if err != nil {
			c.setState(link.Disconnected, "KISS "+addr+" unreachable")
			c.log.Warn("kiss dial failed", "addr", addr, "err", err, "retry_in", backoff)
			select {
			case <-ctx.Done():
				return
			case <-c.wake:
				backoff = c.reconnect.Initial
			case <-time.After(backoff):
				backoff = nextBackoff(backoff, c.reconnect.Max)
			}
			continue
		}
		c.log.Info("kiss connected", "addr", addr)
		c.mu.Lock()
		c.conn = conn
		c.mu.Unlock()
		c.setState(link.Connected, "KISS TNC at "+addr)
		backoff = c.reconnect.Initial

		c.drainUntilClose(ctx, conn)

		c.mu.Lock()
		c.conn = nil
		c.mu.Unlock()
		_ = conn.Close()
		c.setState(link.Disconnected, "KISS "+addr+" disconnected")
		c.log.Info("kiss disconnected")
	}
}

// Send implements link.Link: encodes the AX.25 UI frame, wraps it in KISS
// and writes it.
func (c *Client) Send(ctx context.Context, p link.Packet) error {
	frame, err := ax25.EncodeUI(p.Dest, p.Source, p.Path, []byte(p.Info))
	if err != nil {
		return fmt.Errorf("encode AX.25: %w", err)
	}
	return c.SendAX25(frame)
}

// Retire implements link.Link; KISS keeps no per-object state.
func (c *Client) Retire(ctx context.Context, object string) error { return nil }

// SendAX25 frames an AX.25 frame as a KISS data frame and writes it.
// Returns link.ErrNotConnected if the client isn't currently connected.
func (c *Client) SendAX25(ax25Frame []byte) error {
	c.mu.Lock()
	conn, port := c.conn, c.port
	c.mu.Unlock()
	if conn == nil {
		return link.ErrNotConnected
	}
	frame, err := EncodeDataFrame(port, ax25Frame)
	if err != nil {
		return fmt.Errorf("kiss encode: %w", err)
	}
	if err := conn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return fmt.Errorf("kiss set deadline: %w", err)
	}
	if _, err := conn.Write(frame); err != nil {
		return fmt.Errorf("kiss write: %w", err)
	}
	return nil
}

func (c *Client) setState(st link.Status, detail string) {
	c.mu.Lock()
	prev := c.state
	c.state = link.State{Transport: "kiss", Detail: detail}.WithStatus(st)
	cur := c.state
	c.mu.Unlock()
	if prev.Status != cur.Status || prev.Detail != cur.Detail {
		c.onState(cur)
	}
}

func (c *Client) drainUntilClose(ctx context.Context, conn net.Conn) {
	r := NewReader(conn)
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
				c.log.Debug("kiss read error, dropping connection", "err", err)
			}
			return
		}
		// Frame received from the TNC: ignored, we're send-only.
	}
}

func nextBackoff(d, max time.Duration) time.Duration {
	d *= 2
	if d > max {
		d = max
	}
	return d
}
