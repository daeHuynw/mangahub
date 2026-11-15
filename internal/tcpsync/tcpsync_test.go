package tcpsync

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"testing"
	"time"
)

var tokens = map[string]int64{"alice-1": 1, "alice-2": 1, "alice-3": 1, "bob": 2}

func fakeVerify(tok string) (int64, error) {
	if uid, ok := tokens[tok]; ok {
		return uid, nil
	}
	return 0, errors.New("invalid token")
}

func startServer(t *testing.T) (*Server, string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(NewHub(), fakeVerify)
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return srv, ln.Addr().String()
}

type client struct {
	conn net.Conn
	r    *bufio.Scanner
	sid  string
}

func dial(t *testing.T, addr string) *client {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return &client{conn: c, r: bufio.NewScanner(c)}
}

func (c *client) send(t *testing.T, ev Event) {
	t.Helper()
	if _, err := c.conn.Write(encode(ev)); err != nil {
		t.Fatal(err)
	}
}

// read waits up to d for the next event; ok=false on timeout/EOF.
func (c *client) read(d time.Duration) (Event, bool) {
	c.conn.SetReadDeadline(time.Now().Add(d))
	if !c.r.Scan() {
		return Event{}, false
	}
	var ev Event
	json.Unmarshal(c.r.Bytes(), &ev)
	return ev, true
}

func login(t *testing.T, addr, token string) *client {
	t.Helper()
	c := dial(t, addr)
	c.send(t, Event{Type: "auth", Token: token})
	ev, ok := c.read(time.Second)
	if !ok || ev.Type != "auth_ok" || ev.SessionID == "" {
		t.Fatalf("login %s: %+v ok=%v", token, ev, ok)
	}
	c.sid = ev.SessionID
	return c
}

func waitSessions(t *testing.T, h *Hub, uid int64, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if h.Sessions(uid) == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("user %d has %d sessions, want %d", uid, h.Sessions(uid), want)
}

func TestSameUserOtherSessionReceives(t *testing.T) {
	srv, addr := startServer(t)
	a := login(t, addr, "alice-1")
	b := login(t, addr, "alice-2")
	bob := login(t, addr, "bob")
	waitSessions(t, srv.Hub, 1, 2)

	n := srv.Hub.SendToUser(1, ProgressEvent("one-piece", 25, "reading", 3), a.sid)
	if n != 1 {
		t.Fatalf("delivered to %d sessions, want 1", n)
	}
	ev, ok := b.read(time.Second)
	if !ok || ev.Type != "progress_updated" || ev.MangaID != "one-piece" || *ev.CurrentChapter != 25 || ev.Version != 3 {
		t.Fatalf("session B got %+v ok=%v", ev, ok)
	}
	if ev, ok := a.read(200 * time.Millisecond); ok {
		t.Fatalf("sending session A must not receive its own event, got %+v", ev)
	}
	if ev, ok := bob.read(200 * time.Millisecond); ok {
		t.Fatalf("another user received a private event: %+v", ev)
	}
}

func TestBadToken(t *testing.T) {
	srv, addr := startServer(t)
	c := dial(t, addr)
	c.send(t, Event{Type: "auth", Token: "forged"})
	ev, ok := c.read(time.Second)
	if !ok || ev.Type != "auth_error" {
		t.Fatalf("got %+v ok=%v", ev, ok)
	}
	if _, ok := c.read(time.Second); ok {
		t.Fatal("server should close the connection after auth_error")
	}
	if srv.Hub.Sessions(0) != 0 {
		t.Fatal("unauthenticated session registered")
	}
}

func TestFirstMessageMustBeAuth(t *testing.T) {
	_, addr := startServer(t)
	c := dial(t, addr)
	c.send(t, Event{Type: "ping"})
	ev, ok := c.read(time.Second)
	if !ok || ev.Type != "auth_error" {
		t.Fatalf("got %+v ok=%v", ev, ok)
	}
}

func TestMissingNewlineTimesOut(t *testing.T) {
	_, addr := startServer(t)
	c := dial(t, addr)
	// No '\n' → the server never sees a complete line and closes after AuthTimeout.
	c.conn.Write([]byte(`{"type":"auth","token":"alice-1"}`))
	start := time.Now()
	if _, ok := c.read(AuthTimeout + 2*time.Second); ok {
		t.Fatal("got a reply without a newline-terminated auth line")
	}
	if el := time.Since(start); el < AuthTimeout-500*time.Millisecond {
		t.Fatalf("closed too early: %v", el)
	}
}

func TestDisconnectRemovesSession(t *testing.T) {
	srv, addr := startServer(t)
	a := login(t, addr, "alice-1")
	b := login(t, addr, "alice-2")
	waitSessions(t, srv.Hub, 1, 2)
	b.conn.Close()
	waitSessions(t, srv.Hub, 1, 1)
	if n := srv.Hub.SendToUser(1, ProgressEvent("x", 1, "", 2), a.sid); n != 0 {
		t.Fatalf("event sent to %d sessions after disconnect", n)
	}
}

func TestPingPong(t *testing.T) {
	_, addr := startServer(t)
	a := login(t, addr, "alice-1")
	a.send(t, Event{Type: "ping"})
	if ev, ok := a.read(time.Second); !ok || ev.Type != "pong" {
		t.Fatalf("got %+v ok=%v", ev, ok)
	}
}

// Many sessions + concurrent sends must be race-free (run with -race).
func TestConcurrentSends(t *testing.T) {
	srv, addr := startServer(t)
	clients := []*client{login(t, addr, "alice-1"), login(t, addr, "alice-2"), login(t, addr, "alice-3")}
	waitSessions(t, srv.Hub, 1, 3)
	done := make(chan struct{})
	for i := 0; i < 10; i++ {
		go func(v int) {
			srv.Hub.SendToUser(1, ProgressEvent("m", v, "", int64(v)), "")
			done <- struct{}{}
		}(i)
	}
	for i := 0; i < 10; i++ {
		<-done
	}
	for _, c := range clients {
		for i := 0; i < 10; i++ {
			if _, ok := c.read(time.Second); !ok {
				t.Fatalf("session %s missing event %d", c.sid, i)
			}
		}
	}
}
