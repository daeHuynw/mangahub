// Package chat is the WebSocket discussion service (contracts.md §6, F05),
// mounted by the Gin backend at GET /ws.
//
// Layout:
//   - one hub goroutine owns every room and every client's current room, so
//     there is no shared map and no mutex; clients talk to it over channels
//   - each connection has a read goroutine (decodes JSON, sends it to the hub)
//     and exactly one write goroutine (gorilla/websocket forbids concurrent
//     writes), fed by a buffered channel
//   - a client that cannot keep up (buffer full) is dropped instead of
//     blocking the hub for everyone else
//
// The JWT is checked before the upgrade (bad token → 401 JSON, no socket).
// The sender name always comes from the token; a "from" sent by the client is
// ignored. Rooms are enforced on the server: a client only receives messages
// of the room it joined.
package chat

import (
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"mangahub/internal/auth"
)

// DefaultRoom is joined automatically on connect.
const DefaultRoom = "general"

// MaxText is the longest chat text accepted, in characters.
const MaxText = 500

const (
	writeWait  = 10 * time.Second
	sendBuffer = 32
	readLimit  = 4096 // bytes per incoming frame; a 500-char message fits easily
)

var roomRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

// Verifier turns a JWT into the user's id and name.
type Verifier func(token string) (uid int64, username string, err error)

// JWTVerifier adapts the backend's auth.Manager.
func JWTVerifier(m *auth.Manager) Verifier {
	return func(tok string) (int64, string, error) {
		c, err := m.Verify(tok)
		if err != nil {
			return 0, "", err
		}
		return c.UID, c.Username, nil
	}
}

// Message is the wire format in both directions.
type Message struct {
	Type string `json:"type"`           // join | chat | system | error
	Room string `json:"room,omitempty"` // target or source room
	From string `json:"from,omitempty"` // set by the server only
	Text string `json:"text,omitempty"`
	At   string `json:"at,omitempty"` // RFC3339, set by the server
}

type client struct {
	uid  int64
	name string
	conn *websocket.Conn
	send chan []byte
	room string // owned by the hub goroutine
}

type inbound struct {
	c   *client
	msg Message
}

// Server is the chat service. Create it with New; it is safe for concurrent use.
type Server struct {
	verify   Verifier
	upgrader websocket.Upgrader

	register   chan *client
	unregister chan *client
	inbound    chan inbound
	query      chan func(map[string]map[*client]bool)
	done       chan struct{}

	pongWait   time.Duration // no pong (or message) for this long → connection is dead
	pingPeriod time.Duration // must be shorter than pongWait
}

// New starts the hub goroutine.
func New(verify Verifier) *Server {
	s := &Server{
		verify:     verify,
		upgrader:   websocket.Upgrader{ReadBufferSize: 1024, WriteBufferSize: 1024},
		register:   make(chan *client),
		unregister: make(chan *client),
		inbound:    make(chan inbound, 64),
		query:      make(chan func(map[string]map[*client]bool)),
		done:       make(chan struct{}),
		pongWait:   60 * time.Second,
		pingPeriod: 50 * time.Second,
	}
	go s.run()
	return s
}

// Close stops the hub and closes every connection.
func (s *Server) Close() {
	select {
	case <-s.done:
	default:
		close(s.done)
	}
}

// Members returns how many clients are in room right now (status/tests).
func (s *Server) Members(room string) int {
	n := make(chan int, 1)
	select {
	case s.query <- func(rooms map[string]map[*client]bool) { n <- len(rooms[room]) }:
		return <-n
	case <-s.done:
		return 0
	}
}

// Handler upgrades GET /ws after checking the JWT (?token= or Authorization).
func (s *Server) Handler() gin.HandlerFunc {
	return func(c *gin.Context) {
		tok := auth.TokenFromRequest(c.Request)
		if tok == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing token"})
			return
		}
		uid, name, err := s.verify(tok)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired token"})
			return
		}
		conn, err := s.upgrader.Upgrade(c.Writer, c.Request, nil)
		if err != nil {
			return // the upgrader already answered (400 etc.)
		}
		cl := &client{uid: uid, name: name, conn: conn, send: make(chan []byte, sendBuffer)}
		select {
		case s.register <- cl:
		case <-s.done:
			conn.Close()
			return
		}
		go s.writePump(cl)
		s.readPump(cl) // runs until the connection ends
	}
}

// ---------------------------------------------------------------- hub

func (s *Server) run() {
	rooms := map[string]map[*client]bool{}

	add := func(c *client, room string) {
		if rooms[room] == nil {
			rooms[room] = map[*client]bool{}
		}
		rooms[room][c] = true
		c.room = room
	}
	remove := func(c *client) bool {
		set := rooms[c.room]
		if !set[c] {
			return false
		}
		delete(set, c)
		if len(set) == 0 {
			delete(rooms, c.room)
		}
		return true
	}
	var drop func(c *client)
	sendTo := func(c *client, m Message) {
		select {
		case c.send <- encode(m):
		default:
			drop(c) // too slow: do not let one client block the room
		}
	}
	broadcast := func(room string, m Message) {
		for c := range rooms[room] {
			sendTo(c, m)
		}
	}
	drop = func(c *client) {
		if remove(c) {
			close(c.send) // writePump sends a close frame and exits
			log.Printf("chat: dropped slow client %s in %s", c.name, c.room)
		}
	}
	system := func(room, text string) Message {
		return Message{Type: "system", Room: room, Text: text, At: now()}
	}

	for {
		select {
		case <-s.done:
			for _, set := range rooms {
				for c := range set {
					close(c.send)
				}
			}
			return

		case c := <-s.register:
			add(c, DefaultRoom)
			broadcast(DefaultRoom, system(DefaultRoom, c.name+" joined"))
			log.Printf("chat: %s connected (%s: %d)", c.name, DefaultRoom, len(rooms[DefaultRoom]))

		case c := <-s.unregister:
			room := c.room
			if remove(c) {
				close(c.send)
				broadcast(room, system(room, c.name+" left"))
				log.Printf("chat: %s disconnected from %s", c.name, room)
			}

		case in := <-s.inbound:
			c, m := in.c, in.msg
			if _, ok := rooms[c.room][c]; !ok {
				continue // already removed (dropped); ignore late messages
			}
			switch m.Type {
			case "join":
				room := strings.TrimSpace(m.Room)
				if !roomRe.MatchString(room) {
					sendTo(c, Message{Type: "error", Text: "room must be 1-40 lowercase letters, digits or dashes"})
					continue
				}
				if room == c.room {
					continue
				}
				old := c.room
				remove(c)
				broadcast(old, system(old, c.name+" left"))
				add(c, room)
				broadcast(room, system(room, c.name+" joined"))
			case "chat":
				if m.Room != "" && m.Room != c.room {
					sendTo(c, Message{Type: "error", Text: "you are not in room " + m.Room})
					continue
				}
				text := strings.TrimSpace(m.Text)
				if text == "" || utf8.RuneCountInString(text) > MaxText {
					sendTo(c, Message{Type: "error", Text: "text must be 1-500 characters"})
					continue
				}
				broadcast(c.room, Message{Type: "chat", Room: c.room, From: c.name, Text: text, At: now()})
			default:
				sendTo(c, Message{Type: "error", Text: "unknown message type " + m.Type})
			}

		case f := <-s.query:
			f(rooms)
		}
	}
}

// ---------------------------------------------------------------- pumps

// readPump decodes frames and forwards them to the hub. A missing pong for
// pongWait, a closed socket or an oversized frame ends the connection.
func (s *Server) readPump(c *client) {
	defer func() {
		select {
		case s.unregister <- c:
		case <-s.done:
		}
		c.conn.Close()
	}()
	c.conn.SetReadLimit(readLimit)
	c.conn.SetReadDeadline(time.Now().Add(s.pongWait))
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(s.pongWait))
	})
	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) &&
				!errors.Is(err, net.ErrClosed) {
				log.Printf("chat: %s read: %v", c.name, err)
			}
			return
		}
		c.conn.SetReadDeadline(time.Now().Add(s.pongWait)) // any frame proves the peer is alive
		var m Message
		if err := json.Unmarshal(data, &m); err != nil {
			m = Message{Type: "invalid"} // answered with an error by the hub
		}
		m.From = "" // never trust the client's name
		select {
		case s.inbound <- inbound{c: c, msg: m}:
		case <-s.done:
			return
		}
	}
}

// writePump is the only goroutine that writes to c.conn.
func (s *Server) writePump(c *client) {
	ticker := time.NewTicker(s.pingPeriod)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()
	for {
		select {
		case b, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok { // hub removed us
				c.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, b); err != nil {
				return // readPump sees the closed socket and unregisters
			}
		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func encode(m Message) []byte {
	b, _ := json.Marshal(m)
	return b
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }
