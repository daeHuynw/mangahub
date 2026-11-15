// Package tcpsync pushes reading-progress events to the other TCP sessions of
// the same user (feature F03).
//
// Framing: TCP is a byte stream, so every message is one JSON object followed
// by '\n'. The first line a client sends must be {"type":"auth","token":...}
// within AuthTimeout, otherwise the connection is closed.
package tcpsync

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

const (
	AuthTimeout  = 5 * time.Second
	WriteTimeout = 5 * time.Second
	sendBuffer   = 16
	maxLineBytes = 64 * 1024
)

// Event is the single wire format for every TCP message (see contracts.md §4).
type Event struct {
	Type           string `json:"type"`
	Token          string `json:"token,omitempty"`
	SessionID      string `json:"session_id,omitempty"`
	Error          string `json:"error,omitempty"`
	MangaID        string `json:"manga_id,omitempty"`
	CurrentChapter *int   `json:"current_chapter,omitempty"`
	Status         string `json:"status,omitempty"`
	Version        int64  `json:"version,omitempty"`
}

// ProgressEvent builds a progress_updated event.
func ProgressEvent(mangaID string, chapter int, status string, version int64) Event {
	return Event{Type: "progress_updated", MangaID: mangaID, CurrentChapter: &chapter, Status: status, Version: version}
}

func encode(ev Event) []byte {
	b, _ := json.Marshal(ev)
	return append(b, '\n')
}

// Verifier turns a JWT into a user id. *auth.Manager satisfies it via an adapter
// in cmd/backend; tests use a fake.
type Verifier func(token string) (uid int64, err error)

type session struct {
	id   string
	uid  int64
	conn net.Conn
	send chan []byte
}

// Hub indexes live sessions by user id → session id.
type Hub struct {
	mu    sync.RWMutex
	users map[int64]map[string]*session
}

func NewHub() *Hub { return &Hub{users: map[int64]map[string]*session{}} }

func (h *Hub) add(s *session) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.users[s.uid] == nil {
		h.users[s.uid] = map[string]*session{}
	}
	h.users[s.uid][s.id] = s
	log.Printf("tcpsync: user %d +%s (%d session(s))", s.uid, s.id, len(h.users[s.uid]))
}

func (h *Hub) remove(s *session) {
	h.mu.Lock()
	defer h.mu.Unlock()
	set := h.users[s.uid]
	if set == nil || set[s.id] != s {
		return
	}
	delete(set, s.id)
	close(s.send) // stops writeLoop; only ever closed here, under the lock
	if len(set) == 0 {
		delete(h.users, s.uid)
	}
	log.Printf("tcpsync: user %d -%s", s.uid, s.id)
}

// SendToUser delivers ev to every session of uid except the one whose id is
// `except` (the session that made the HTTP change). It never blocks: a session
// whose buffer is full is skipped. Returns how many sessions got the event.
func (h *Hub) SendToUser(uid int64, ev Event, except string) int {
	msg := encode(ev)
	h.mu.RLock()
	defer h.mu.RUnlock()
	n := 0
	for id, s := range h.users[uid] {
		if id == except {
			continue
		}
		select {
		case s.send <- msg:
			n++
		default:
			log.Printf("tcpsync: session %s too slow, event dropped", id)
		}
	}
	return n
}

// Sessions returns how many sessions uid currently has (for tests/status).
func (h *Hub) Sessions(uid int64) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.users[uid])
}

// Server accepts TCP connections and registers authenticated sessions in Hub.
type Server struct {
	Hub    *Hub
	verify Verifier
	seq    atomic.Int64

	mu     sync.Mutex
	ln     net.Listener
	conns  map[net.Conn]struct{}
	closed bool
	wg     sync.WaitGroup
}

func NewServer(hub *Hub, verify Verifier) *Server {
	return &Server{Hub: hub, verify: verify, conns: map[net.Conn]struct{}{}}
}

// ListenAndServe listens on addr and serves until Close.
func (s *Server) ListenAndServe(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	return s.Serve(ln)
}

// Serve accepts connections on ln until Close is called.
func (s *Server) Serve(ln net.Listener) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		ln.Close()
		return net.ErrClosed
	}
	s.ln = ln
	s.mu.Unlock()

	log.Printf("tcpsync: listening on %s", ln.Addr())
	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			log.Printf("tcpsync: accept: %v", err)
			time.Sleep(50 * time.Millisecond)
			continue
		}
		if !s.track(conn) {
			conn.Close()
			return nil
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer s.untrack(conn)
			s.handle(conn)
		}()
	}
}

func (s *Server) track(c net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.conns[c] = struct{}{}
	return true
}

func (s *Server) untrack(c net.Conn) {
	s.mu.Lock()
	delete(s.conns, c)
	s.mu.Unlock()
}

// Close stops accepting, closes every connection and waits for handlers.
func (s *Server) Close() error {
	s.mu.Lock()
	s.closed = true
	if s.ln != nil {
		s.ln.Close()
	}
	for c := range s.conns {
		c.Close()
	}
	s.mu.Unlock()
	s.wg.Wait()
	return nil
}

func (s *Server) handle(conn net.Conn) {
	defer conn.Close()
	if tc, ok := conn.(*net.TCPConn); ok {
		tc.SetKeepAlive(true)
		tc.SetKeepAlivePeriod(30 * time.Second)
	}
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 4096), maxLineBytes)

	// 1) First line must authenticate within AuthTimeout.
	conn.SetReadDeadline(time.Now().Add(AuthTimeout))
	if !sc.Scan() {
		log.Printf("tcpsync: %s sent no auth line: %v", conn.RemoteAddr(), sc.Err())
		return
	}
	var first Event
	if err := json.Unmarshal(sc.Bytes(), &first); err != nil || first.Type != "auth" {
		writeNow(conn, Event{Type: "auth_error", Error: "first message must be auth"})
		return
	}
	uid, err := s.verify(first.Token)
	if err != nil {
		writeNow(conn, Event{Type: "auth_error", Error: "invalid token"})
		return
	}
	conn.SetReadDeadline(time.Time{}) // authenticated: no idle deadline (keepalive detects dead peers)

	// 2) Register; the write goroutine owns all further writes to conn.
	sess := &session{id: fmt.Sprintf("s%d", s.seq.Add(1)), uid: uid, conn: conn, send: make(chan []byte, sendBuffer)}
	sess.send <- encode(Event{Type: "auth_ok", SessionID: sess.id})
	s.Hub.add(sess)
	defer s.Hub.remove(sess)
	go writeLoop(sess)

	// 3) Read loop: detects disconnect (EOF) and answers ping.
	for sc.Scan() {
		var ev Event
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue // ignore malformed lines, keep the session
		}
		if ev.Type == "ping" {
			// Safe: send is closed only by remove(), which runs after this loop
			// in this same goroutine.
			select {
			case sess.send <- encode(Event{Type: "pong"}):
			default:
			}
		}
	}
}

func writeNow(conn net.Conn, ev Event) {
	conn.SetWriteDeadline(time.Now().Add(WriteTimeout))
	conn.Write(encode(ev))
}

func writeLoop(s *session) {
	for msg := range s.send {
		s.conn.SetWriteDeadline(time.Now().Add(WriteTimeout))
		if _, err := s.conn.Write(msg); err != nil {
			s.conn.Close() // makes the read loop fail → remove → close(send)
		}
	}
}
