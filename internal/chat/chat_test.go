package chat

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// Token "<name>" is user <name>; anything else is rejected.
func verify(tok string) (int64, string, error) {
	switch tok {
	case "alice":
		return 1, "alice", nil
	case "bob":
		return 2, "bob", nil
	case "charlie":
		return 3, "charlie", nil
	}
	if strings.HasPrefix(tok, "user") {
		return 100, tok, nil
	}
	return 0, "", errors.New("bad token")
}

func startServer(t *testing.T, tune func(*Server)) (*Server, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	s := New(verify)
	if tune != nil {
		tune(s)
	}
	r := gin.New()
	r.GET("/ws", s.Handler())
	ts := httptest.NewServer(r)
	t.Cleanup(func() { ts.Close(); s.Close() })
	return s, "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
}

type peer struct {
	t    *testing.T
	name string
	conn *websocket.Conn
}

func dial(t *testing.T, url, token string) *peer {
	t.Helper()
	conn, resp, err := websocket.DefaultDialer.Dial(url+"?token="+token, nil)
	if err != nil {
		code := 0
		if resp != nil {
			code = resp.StatusCode
		}
		t.Fatalf("dial %s: %v (HTTP %d)", token, err, code)
	}
	t.Cleanup(func() { conn.Close() })
	return &peer{t: t, name: token, conn: conn}
}

func (p *peer) send(m Message) {
	p.t.Helper()
	if err := p.conn.WriteJSON(m); err != nil {
		p.t.Fatalf("%s send: %v", p.name, err)
	}
}

// next reads one message or fails after 2 s.
func (p *peer) next() Message {
	p.t.Helper()
	p.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var m Message
	if err := p.conn.ReadJSON(&m); err != nil {
		p.t.Fatalf("%s read: %v", p.name, err)
	}
	return m
}

// expect skips messages until one matches typ and contains text.
func (p *peer) expect(typ, text string) Message {
	p.t.Helper()
	for i := 0; i < 20; i++ {
		m := p.next()
		if m.Type == typ && strings.Contains(m.Text, text) {
			return m
		}
	}
	p.t.Fatalf("%s never got %s %q", p.name, typ, text)
	return Message{}
}

// nextChat returns the next chat message, skipping system messages.
func (p *peer) nextChat() Message {
	p.t.Helper()
	for {
		if m := p.next(); m.Type == "chat" {
			return m
		}
	}
}

// silent asserts nothing arrives within d. Gorilla makes a read timeout
// permanent, so this must be the last read on the connection.
func (p *peer) silent(d time.Duration) {
	p.t.Helper()
	p.conn.SetReadDeadline(time.Now().Add(d))
	var m Message
	if err := p.conn.ReadJSON(&m); err == nil {
		p.t.Fatalf("%s got unexpected %+v", p.name, m)
	}
}

func waitMembers(t *testing.T, s *Server, room string, n int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for s.Members(room) != n {
		if time.Now().After(deadline) {
			t.Fatalf("room %s has %d members, want %d", room, s.Members(room), n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAuthBeforeUpgrade(t *testing.T) {
	_, url := startServer(t, nil)
	for _, tok := range []string{"", "forged"} {
		_, resp, err := websocket.DefaultDialer.Dial(url+"?token="+tok, nil)
		if err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("token %q: err=%v resp=%v, want 401", tok, err, resp)
		}
		var body map[string]string
		json.NewDecoder(resp.Body).Decode(&body)
		if body["error"] == "" {
			t.Fatalf("401 body = %v, want {\"error\": ...}", body)
		}
	}
	// Authorization header works too (contract: query or header).
	h := http.Header{"Authorization": {"Bearer alice"}}
	conn, _, err := websocket.DefaultDialer.Dial(url, h)
	if err != nil {
		t.Fatalf("header token: %v", err)
	}
	conn.Close()
}

// F05 demo: Alice and Bob talk in general; the sender name comes from the token.
func TestAliceAndBobExchange(t *testing.T) {
	s, url := startServer(t, nil)
	alice := dial(t, url, "alice")
	alice.expect("system", "alice joined")
	bob := dial(t, url, "bob")
	alice.expect("system", "bob joined")
	bob.expect("system", "bob joined")
	waitMembers(t, s, DefaultRoom, 2)

	alice.send(Message{Type: "chat", Room: "general", From: "bob", Text: "  Chapter 26 is out!  "})
	for _, p := range []*peer{alice, bob} {
		m := p.expect("chat", "Chapter 26")
		if m.From != "alice" || m.Room != "general" || m.Text != "Chapter 26 is out!" || m.At == "" {
			t.Fatalf("%s got %+v (from must be alice, text trimmed, at set)", p.name, m)
		}
	}
	bob.send(Message{Type: "chat", Text: "nice"}) // room omitted = current room
	alice.expect("chat", "nice")
}

// F05: Charlie in another room sees nothing from general and vice versa.
func TestRoomIsolation(t *testing.T) {
	s, url := startServer(t, nil)
	alice := dial(t, url, "alice")
	alice.expect("system", "alice joined")
	charlie := dial(t, url, "charlie")
	charlie.expect("system", "charlie joined")
	charlie.send(Message{Type: "join", Room: "one-piece"})
	charlie.expect("system", "charlie joined") // joined one-piece
	alice.expect("system", "charlie left")     // left general
	waitMembers(t, s, "one-piece", 1)
	waitMembers(t, s, DefaultRoom, 1)

	// The hub handles messages in order. Once Alice has her own "general
	// only" back, it was already broadcast; so if Charlie's next chat is his
	// own message, he never got Alice's. (A read timeout cannot be used
	// mid-test: gorilla treats it as fatal for the connection.)
	alice.send(Message{Type: "chat", Text: "general only"})
	alice.expect("chat", "general only")
	charlie.send(Message{Type: "chat", Room: "one-piece", Text: "one-piece only"})
	if m := charlie.nextChat(); m.Text != "one-piece only" {
		t.Fatalf("charlie got %+v from another room", m)
	}
	alice.send(Message{Type: "chat", Text: "marker"})
	if m := alice.nextChat(); m.Text != "marker" {
		t.Fatalf("alice got %+v from another room", m)
	}
	charlie.silent(300 * time.Millisecond) // last read on this connection
}

// F05: Bob disconnects; he is removed and the hub keeps serving Alice.
func TestDisconnectIsCleanedUp(t *testing.T) {
	s, url := startServer(t, nil)
	alice := dial(t, url, "alice")
	alice.expect("system", "alice joined")
	bob := dial(t, url, "bob")
	alice.expect("system", "bob joined")
	waitMembers(t, s, DefaultRoom, 2)

	bob.conn.Close() // no close frame: like a killed TUI
	alice.expect("system", "bob left")
	waitMembers(t, s, DefaultRoom, 1)

	alice.send(Message{Type: "chat", Text: "still here"})
	alice.expect("chat", "still here")
}

func TestValidationErrorsKeepTheConnection(t *testing.T) {
	_, url := startServer(t, nil)
	alice := dial(t, url, "alice")
	alice.expect("system", "alice joined")

	cases := []struct {
		msg  Message
		want string
	}{
		{Message{Type: "chat", Text: "   "}, "1-500"},
		{Message{Type: "chat", Text: strings.Repeat("x", MaxText+1)}, "1-500"},
		{Message{Type: "chat", Room: "naruto", Text: "hi"}, "you are not in room naruto"},
		{Message{Type: "join", Room: "Bad Room!"}, "room must be"},
		{Message{Type: "join", Room: ""}, "room must be"},
		{Message{Type: "dance"}, "unknown message type"},
	}
	for _, tc := range cases {
		alice.send(tc.msg)
		if m := alice.next(); m.Type != "error" || !strings.Contains(m.Text, tc.want) {
			t.Fatalf("%+v -> %+v, want error %q", tc.msg, m, tc.want)
		}
	}
	alice.conn.WriteMessage(websocket.TextMessage, []byte("{not json"))
	if m := alice.next(); m.Type != "error" {
		t.Fatalf("bad JSON -> %+v, want error", m)
	}
	// Limits count characters, not bytes: 500 "ä" (1000 bytes) is fine.
	alice.send(Message{Type: "chat", Text: strings.Repeat("ä", MaxText)})
	if m := alice.next(); m.Type != "chat" {
		t.Fatalf("500 characters -> %+v", m)
	}
}

func TestJoinSameRoomIsQuiet(t *testing.T) {
	_, url := startServer(t, nil)
	alice := dial(t, url, "alice")
	alice.expect("system", "alice joined")
	alice.send(Message{Type: "join", Room: "general"})
	alice.silent(200 * time.Millisecond)
}

// Many clients chatting at once (run with -race): everybody gets everything.
func TestManyClientsConcurrently(t *testing.T) {
	s, url := startServer(t, nil)
	const n, each = 10, 5
	peers := make([]*peer, n)
	for i := range peers {
		peers[i] = dial(t, url, fmt.Sprintf("user%d", i))
	}
	waitMembers(t, s, DefaultRoom, n)

	var wg sync.WaitGroup
	for i, p := range peers {
		wg.Add(1)
		go func(i int, p *peer) {
			defer wg.Done()
			for k := 0; k < each; k++ {
				p.conn.WriteJSON(Message{Type: "chat", Text: fmt.Sprintf("m%d-%d", i, k)})
			}
		}(i, p)
	}
	wg.Wait()
	for _, p := range peers {
		got := 0
		p.conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		for got < n*each {
			var m Message
			if err := p.conn.ReadJSON(&m); err != nil {
				t.Fatalf("%s: %v after %d chats", p.name, err, got)
			}
			if m.Type == "chat" {
				got++
			}
		}
	}
}

// A peer that stops answering pings (frozen process, cable pulled) is
// removed after pongWait even though no close frame ever arrives.
func TestDeadPeerIsRemoved(t *testing.T) {
	s, url := startServer(t, func(s *Server) {
		s.pongWait = 300 * time.Millisecond
		s.pingPeriod = 100 * time.Millisecond
	})
	// This client never reads, so gorilla never answers the server's pings.
	conn, _, err := websocket.DefaultDialer.Dial(url+"?token=bob", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	waitMembers(t, s, DefaultRoom, 1)
	waitMembers(t, s, DefaultRoom, 0)
}

// A peer that does read answers pings automatically and stays connected.
func TestPingPongKeepsLivePeer(t *testing.T) {
	s, url := startServer(t, func(s *Server) {
		s.pongWait = 300 * time.Millisecond
		s.pingPeriod = 100 * time.Millisecond
	})
	alice := dial(t, url, "alice")
	go func() { // keep reading so pings get answered
		for {
			if _, _, err := alice.conn.ReadMessage(); err != nil {
				return
			}
		}
	}()
	time.Sleep(time.Second) // > 3x pongWait
	if s.Members(DefaultRoom) != 1 {
		t.Fatal("a live peer was dropped")
	}
}

func TestCloseDisconnectsEveryone(t *testing.T) {
	s, url := startServer(t, nil)
	alice := dial(t, url, "alice")
	alice.expect("system", "alice joined")
	s.Close()
	alice.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, err := alice.conn.ReadMessage()
	if !websocket.IsCloseError(err, websocket.CloseNormalClosure) {
		t.Fatalf("err = %v, want a normal close frame", err)
	}
}
