// Package wsclient is the TUI side of the room chat (contracts.md §6, F05).
//
// One WebSocket to GET /ws. The JWT goes in the Authorization header, not
// in ?token=, so it does not end up in the backend's request log. The server
// puts every new connection in "general"; the client remembers the room the
// user picked and joins it again after a reconnect.
//
// Writes: gorilla allows one writer at a time, so Join/Say only queue the
// message and a single writer goroutine per connection sends it. Pings from
// the server are answered from the read loop; silence for ReadTimeout means
// the connection is dead and Run reconnects with a growing delay.
package wsclient

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// State of the connection, reported through Handler.OnState.
type State int

const (
	Connecting State = iota
	Connected
	Disconnected // lost; Run retries after a delay
	Rejected     // 401: token refused, Run stops
	Stopped      // ctx cancelled
)

func (s State) String() string {
	return [...]string{"connecting", "connected", "disconnected", "rejected", "stopped"}[s]
}

// DefaultRoom matches the server's default.
const DefaultRoom = "general"

var (
	ErrNotConnected = errors.New("chat is not connected")
	ErrRejected     = errors.New("chat server rejected the token")
)

// Message is the wire format (same fields as internal/chat.Message).
type Message struct {
	Type string `json:"type"`
	Room string `json:"room,omitempty"`
	From string `json:"from,omitempty"`
	Text string `json:"text,omitempty"`
	At   string `json:"at,omitempty"`
}

// Config for one chat client. Zero durations take the defaults.
type Config struct {
	URL         string        // ws://host:port/ws
	Token       func() string // read on every (re)connect
	ReadTimeout time.Duration // default 75s (server pings every 50s)
	MinBackoff  time.Duration // default 1s
	MaxBackoff  time.Duration // default 10s
}

// Handler callbacks run on Run's goroutine.
type Handler struct {
	OnState   func(s State, err error)
	OnMessage func(m Message)
}

// Client keeps one chat connection alive. Join and Say are safe to call from
// any goroutine.
type Client struct {
	cfg Config
	h   Handler

	mu   sync.Mutex
	room string         // room the user wants to be in
	out  chan<- Message // writer queue of the live connection; nil when offline
}

// New prepares a client; call Run to connect.
func New(cfg Config, h Handler) *Client {
	if cfg.ReadTimeout <= 0 {
		cfg.ReadTimeout = 75 * time.Second
	}
	if cfg.MinBackoff <= 0 {
		cfg.MinBackoff = time.Second
	}
	if cfg.MaxBackoff <= 0 {
		cfg.MaxBackoff = 10 * time.Second
	}
	if h.OnState == nil {
		h.OnState = func(State, error) {}
	}
	if h.OnMessage == nil {
		h.OnMessage = func(Message) {}
	}
	return &Client{cfg: cfg, h: h, room: DefaultRoom}
}

// Room returns the room the user is in (or will rejoin after a reconnect).
func (c *Client) Room() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.room
}

// Join switches room. Offline, it is remembered and sent on reconnect.
func (c *Client) Join(room string) error {
	c.mu.Lock()
	c.room = room
	c.mu.Unlock()
	return c.queue(Message{Type: "join", Room: room})
}

// Say sends a chat message to the current room.
func (c *Client) Say(text string) error {
	return c.queue(Message{Type: "chat", Room: c.Room(), Text: text})
}

func (c *Client) queue(m Message) error {
	c.mu.Lock()
	out := c.out
	c.mu.Unlock()
	if out == nil {
		return ErrNotConnected
	}
	select {
	case out <- m:
		return nil
	default:
		return errors.New("chat send queue is full")
	}
}

// Run connects and reconnects until ctx is cancelled or the token is refused.
func (c *Client) Run(ctx context.Context) {
	backoff := c.cfg.MinBackoff
	for {
		c.h.OnState(Connecting, nil)
		connected, err := c.session(ctx)
		if ctx.Err() != nil {
			c.h.OnState(Stopped, nil)
			return
		}
		if errors.Is(err, ErrRejected) {
			c.h.OnState(Rejected, err)
			return
		}
		if connected {
			backoff = c.cfg.MinBackoff
		}
		c.h.OnState(Disconnected, err)
		select {
		case <-ctx.Done():
			c.h.OnState(Stopped, nil)
			return
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > c.cfg.MaxBackoff {
			backoff = c.cfg.MaxBackoff
		}
	}
}

func (c *Client) session(ctx context.Context) (connected bool, err error) {
	hdr := http.Header{}
	if c.cfg.Token != nil {
		hdr.Set("Authorization", "Bearer "+c.cfg.Token())
	}
	d := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	conn, resp, err := d.DialContext(ctx, c.cfg.URL, hdr)
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			return false, ErrRejected
		}
		return false, err
	}
	defer conn.Close()

	// Cancelling ctx closes the socket, which unblocks ReadJSON below.
	stop := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			conn.WriteControl(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, "bye"), time.Now().Add(time.Second))
			conn.Close()
		case <-stop:
		}
	}()

	// Single writer for this connection.
	out := make(chan Message, 32)
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		for {
			select {
			case <-stop:
				return
			case m := <-out:
				conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
				if conn.WriteJSON(m) != nil {
					conn.Close() // read loop fails → reconnect
					return
				}
			}
		}
	}()
	// Close stop first, then wait for the writer. (As two separate defers
	// they ran in the wrong order: waiting for the writer, which waited for
	// stop. Run never returned.)
	defer func() {
		close(stop)
		<-writerDone
	}()

	// Back in the room the user had picked (the server starts us in general).
	room := c.Room()
	if room != DefaultRoom {
		out <- Message{Type: "join", Room: room}
	}
	c.mu.Lock()
	c.out = out
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.out = nil
		c.mu.Unlock()
	}()
	c.h.OnState(Connected, nil)

	conn.SetReadDeadline(time.Now().Add(c.cfg.ReadTimeout))
	conn.SetPingHandler(func(data string) error {
		conn.SetReadDeadline(time.Now().Add(c.cfg.ReadTimeout))
		return conn.WriteControl(websocket.PongMessage, []byte(data), time.Now().Add(5*time.Second))
	})
	for {
		var m Message
		if err := conn.ReadJSON(&m); err != nil {
			return true, err
		}
		conn.SetReadDeadline(time.Now().Add(c.cfg.ReadTimeout))
		c.h.OnMessage(m)
	}
}
