// Package tcpclient keeps the TUI's TCP session to the backend's progress
// sync server (contracts.md §4, feature F03).
//
// Wire format: TCP is a byte stream, so every message is one JSON object
// followed by '\n'. The first line we send is {"type":"auth","token":...};
// the server answers auth_ok with our session id, then pushes
// progress_updated events made by the user's OTHER sessions.
//
// Run keeps the session alive: it pings every PingInterval, treats silence
// longer than ReadTimeout as a dead connection (half-open sockets do not
// always produce an error), and reconnects with a growing delay. It knows
// nothing about tview; the TUI passes callbacks.
package tcpclient

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"mangahub/internal/tui/client"
)

// State of the connection, reported through Handler.OnState.
type State int

const (
	Connecting   State = iota // dialing or waiting for auth_ok
	Connected                 // authenticated, receiving events
	Disconnected              // lost; Run will retry after a delay
	AuthFailed                // server rejected the token; Run stops
	Stopped                   // ctx cancelled; Run returned
)

func (s State) String() string {
	return [...]string{"connecting", "connected", "disconnected", "auth failed", "stopped"}[s]
}

// ErrAuth is reported with AuthFailed.
var ErrAuth = errors.New("token rejected by the sync server")

// Config controls one session. Zero durations take the defaults below.
type Config struct {
	Addr         string        // e.g. "localhost:9090"
	Token        func() string // read on every (re)connect: the JWT may change
	DialTimeout  time.Duration // default 3s
	AuthTimeout  time.Duration // default 5s (the server's own limit)
	PingInterval time.Duration // default 30s (contract)
	ReadTimeout  time.Duration // default 2.5 × PingInterval
	MinBackoff   time.Duration // default 1s
	MaxBackoff   time.Duration // default 10s
}

func (c *Config) defaults() {
	if c.DialTimeout <= 0 {
		c.DialTimeout = 3 * time.Second
	}
	if c.AuthTimeout <= 0 {
		c.AuthTimeout = 5 * time.Second
	}
	if c.PingInterval <= 0 {
		c.PingInterval = 30 * time.Second
	}
	if c.ReadTimeout <= 0 {
		c.ReadTimeout = c.PingInterval * 5 / 2
	}
	if c.MinBackoff <= 0 {
		c.MinBackoff = time.Second
	}
	if c.MaxBackoff <= 0 {
		c.MaxBackoff = 10 * time.Second
	}
}

// Handler receives what happens. Callbacks run on Run's goroutine, one at a
// time; they must not block for long (the TUI just queues a UI update).
type Handler struct {
	// OnState: sessionID is set for Connected, err for Disconnected/AuthFailed.
	OnState func(s State, sessionID string, err error)
	// OnProgress: a progress_updated event from another session.
	OnProgress func(p client.Progress)
}

// event is the single wire format (same fields as internal/tcpsync.Event).
type event struct {
	Type           string `json:"type"`
	Token          string `json:"token,omitempty"`
	SessionID      string `json:"session_id,omitempty"`
	Error          string `json:"error,omitempty"`
	MangaID        string `json:"manga_id,omitempty"`
	CurrentChapter *int   `json:"current_chapter,omitempty"`
	Status         string `json:"status,omitempty"`
	Version        int64  `json:"version,omitempty"`
}

const maxLine = 64 * 1024 // same limit as the server

// Run connects and keeps reconnecting until ctx is cancelled or the server
// rejects the token. It always ends with OnState(Stopped) or
// OnState(AuthFailed).
func Run(ctx context.Context, cfg Config, h Handler) {
	cfg.defaults()
	if h.OnState == nil {
		h.OnState = func(State, string, error) {}
	}
	if h.OnProgress == nil {
		h.OnProgress = func(client.Progress) {}
	}
	backoff := cfg.MinBackoff
	for {
		h.OnState(Connecting, "", nil)
		connected, err := session(ctx, cfg, h)
		if ctx.Err() != nil {
			h.OnState(Stopped, "", nil)
			return
		}
		if errors.Is(err, ErrAuth) {
			h.OnState(AuthFailed, "", err) // retrying with the same token is pointless
			return
		}
		if connected {
			backoff = cfg.MinBackoff // it worked for a while: retry quickly
		}
		h.OnState(Disconnected, "", err)
		select {
		case <-ctx.Done():
			h.OnState(Stopped, "", nil)
			return
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > cfg.MaxBackoff {
			backoff = cfg.MaxBackoff
		}
	}
}

// session runs one connection until it fails. connected reports whether
// auth_ok was received (used to reset the backoff).
func session(ctx context.Context, cfg Config, h Handler) (connected bool, err error) {
	d := net.Dialer{Timeout: cfg.DialTimeout, KeepAlive: 30 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", cfg.Addr)
	if err != nil {
		return false, err
	}
	defer conn.Close()

	// Closing the socket is the only way to unblock a pending Read, so a
	// watcher closes it when ctx is cancelled.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-stop:
		}
	}()

	var wmu sync.Mutex // the ping goroutine and this one both write
	write := func(ev event) error {
		b, _ := json.Marshal(ev)
		wmu.Lock()
		defer wmu.Unlock()
		conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		_, err := conn.Write(append(b, '\n'))
		return err
	}

	// 1) Authenticate: first line out, first line back.
	token := ""
	if cfg.Token != nil {
		token = cfg.Token()
	}
	if err := write(event{Type: "auth", Token: token}); err != nil {
		return false, fmt.Errorf("send auth: %w", err)
	}
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 4096), maxLine)
	conn.SetReadDeadline(time.Now().Add(cfg.AuthTimeout))
	if !sc.Scan() {
		return false, fmt.Errorf("no auth answer: %w", scanErr(sc))
	}
	var first event
	if err := json.Unmarshal(sc.Bytes(), &first); err != nil {
		return false, fmt.Errorf("bad auth answer: %w", err)
	}
	switch first.Type {
	case "auth_ok":
	case "auth_error":
		return false, fmt.Errorf("%w: %s", ErrAuth, first.Error)
	default:
		return false, fmt.Errorf("unexpected first message %q", first.Type)
	}
	h.OnState(Connected, first.SessionID, nil)

	// 2) Heartbeat: a ping every PingInterval makes the server answer pong,
	// so a healthy connection is never silent for ReadTimeout.
	go func() {
		t := time.NewTicker(cfg.PingInterval)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				if write(event{Type: "ping"}) != nil {
					conn.Close() // makes the read loop fail → reconnect
					return
				}
			}
		}
	}()

	// 3) Read loop: one JSON object per line.
	for {
		conn.SetReadDeadline(time.Now().Add(cfg.ReadTimeout))
		if !sc.Scan() {
			return true, fmt.Errorf("connection lost: %w", scanErr(sc))
		}
		var ev event
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue // skip a malformed line, keep the session
		}
		if ev.Type == "progress_updated" && ev.MangaID != "" && ev.CurrentChapter != nil {
			h.OnProgress(client.Progress{
				MangaID: ev.MangaID, CurrentChapter: *ev.CurrentChapter, Status: ev.Status, Version: ev.Version,
			})
		}
		// pong and unknown types only prove the connection is alive.
	}
}

// scanErr turns "Scan returned false" into an error (EOF has none).
func scanErr(sc *bufio.Scanner) error {
	if err := sc.Err(); err != nil {
		return err
	}
	return errors.New("closed by server (EOF)")
}
