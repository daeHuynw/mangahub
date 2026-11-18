// Package udpclient subscribes the TUI to new-chapter alerts (contracts.md
// §5, feature F04).
//
// One JSON object per datagram. The server remembers the address a
// subscribe came from and forgets it after 90 s of silence, so the client
// re-sends subscribe every Heartbeat. UDP gives no delivery guarantee: an
// ack may be lost (we retry up to MaxTries, AckTimeout apart) and so may a
// new_chapter alert (nothing can be done about that; the library still shows
// the right count after the next HTTP reload).
//
// The socket is "connected" (net.DialUDP), so the local port stays the same
// for the whole session and every heartbeat refreshes the same subscription.
// On Windows a connected UDP socket also reports ICMP "port unreachable" as a
// read error when the server is down; the read loop ignores those errors.
package udpclient

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"time"
)

// State of the subscription, reported through Handler.OnState.
type State int

const (
	Subscribing State = iota // subscribe sent, waiting for the ack
	Subscribed               // server acknowledged
	NoAck                    // MaxTries without an ack; heartbeats keep trying
	Rejected                 // server said the token is invalid; Run stops
	Stopped                  // ctx cancelled; unsubscribe was sent
)

func (s State) String() string {
	return [...]string{"subscribing", "subscribed", "no ack", "rejected", "stopped"}[s]
}

// ErrRejected is reported with Rejected.
var ErrRejected = errors.New("subscription rejected by the server")

// Chapter is one new_chapter alert.
type Chapter struct {
	MangaID       string
	Title         string
	TotalChapters int
}

// Config for one subscription. Zero values take the defaults.
type Config struct {
	Addr       string        // e.g. "localhost:9091"
	Token      func() string // JWT, read on every (re)subscribe
	AckTimeout time.Duration // default 1s (contract)
	MaxTries   int           // default 3 (contract)
	Heartbeat  time.Duration // default 30s (contract; server TTL is 90s)
}

func (c *Config) defaults() {
	if c.AckTimeout <= 0 {
		c.AckTimeout = time.Second
	}
	if c.MaxTries <= 0 {
		c.MaxTries = 3
	}
	if c.Heartbeat <= 0 {
		c.Heartbeat = 30 * time.Second
	}
}

// Handler callbacks run on Run's goroutine, one at a time.
type Handler struct {
	OnState   func(s State, err error)
	OnChapter func(c Chapter)
}

type msg struct {
	Type          string `json:"type"`
	Token         string `json:"token,omitempty"`
	MangaID       string `json:"manga_id,omitempty"`
	Title         string `json:"title,omitempty"`
	TotalChapters int    `json:"total_chapters,omitempty"`
	Error         string `json:"error,omitempty"`
}

const maxDatagram = 2048 // server keeps them under 1200 bytes

// Run subscribes and keeps the subscription alive until ctx is cancelled
// (then it sends unsubscribe) or the server rejects the token.
// It returns an error only if the socket cannot be created at all.
func Run(ctx context.Context, cfg Config, h Handler) error {
	cfg.defaults()
	if h.OnState == nil {
		h.OnState = func(State, error) {}
	}
	if h.OnChapter == nil {
		h.OnChapter = func(Chapter) {}
	}
	raddr, err := net.ResolveUDPAddr("udp", cfg.Addr)
	if err != nil {
		return err
	}
	conn, err := net.DialUDP("udp", nil, raddr)
	if err != nil {
		return err
	}
	defer conn.Close()

	token := func() string {
		if cfg.Token == nil {
			return ""
		}
		return cfg.Token()
	}
	send := func(m msg) {
		b, _ := json.Marshal(m)
		conn.SetWriteDeadline(time.Now().Add(time.Second))
		conn.Write(b) // best effort: an error here is like a lost datagram
	}

	// Reader goroutine: hands decoded datagrams to the loop below.
	in := make(chan msg, 16)
	go func() {
		buf := make([]byte, maxDatagram)
		for {
			n, err := conn.Read(buf)
			if err != nil {
				if errors.Is(err, net.ErrClosed) {
					close(in)
					return
				}
				// e.g. "connection reset" on Windows when nothing listens on
				// the server port yet: not fatal, the next heartbeat retries.
				time.Sleep(10 * time.Millisecond)
				continue
			}
			var m msg
			if json.Unmarshal(buf[:n], &m) != nil {
				continue // malformed datagram: drop it
			}
			select {
			case in <- m:
			default: // loop is busy; dropping is fine for UDP semantics
			}
		}
	}()

	state := State(-1)
	setState := func(s State, err error) {
		if s != state {
			state = s
			h.OnState(s, err)
		}
	}

	tries := 0
	var ackTimer <-chan time.Time
	// lastToken is the token of the last subscribe. Unsubscribe reuses it:
	// on logout the caller clears its token right after cancelling us, and an
	// unsubscribe with an empty token would be rejected, leaving the server
	// sending alerts to this port for another 90 s.
	lastToken := ""
	subscribe := func() {
		lastToken = token()
		send(msg{Type: "subscribe", Token: lastToken})
		tries++
		ackTimer = time.After(cfg.AckTimeout)
	}

	setState(Subscribing, nil)
	subscribe()
	heartbeat := time.NewTicker(cfg.Heartbeat)
	defer heartbeat.Stop()

	for {
		select {
		case <-ctx.Done():
			send(msg{Type: "unsubscribe", Token: lastToken})
			h.OnState(Stopped, nil)
			return nil

		case <-ackTimer:
			if tries < cfg.MaxTries {
				subscribe()
			} else {
				ackTimer = nil
				setState(NoAck, errors.New("no ack from the notification server"))
			}

		case <-heartbeat.C:
			tries = 0
			subscribe()

		case m, ok := <-in:
			if !ok {
				return nil
			}
			switch m.Type {
			case "subscribed":
				ackTimer, tries = nil, 0
				setState(Subscribed, nil)
			case "new_chapter":
				if m.MangaID != "" {
					h.OnChapter(Chapter{MangaID: m.MangaID, Title: m.Title, TotalChapters: m.TotalChapters})
				}
			case "error":
				if m.Error == "invalid token" {
					h.OnState(Rejected, ErrRejected)
					return nil
				}
			}
		}
	}
}
