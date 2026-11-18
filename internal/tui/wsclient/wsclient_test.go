package wsclient

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"mangahub/internal/chat"
)

func verify(tok string) (int64, string, error) {
	switch tok {
	case "alice", "bob", "charlie":
		return int64(len(tok)), tok, nil
	}
	return 0, "", errors.New("bad token")
}

type server struct {
	chat *chat.Server
	http *httptest.Server
	url  string

	mu       sync.Mutex
	rawQuery []string // query strings seen on /ws (the token must not be there)
}

func startServer(t *testing.T, ln net.Listener) *server {
	t.Helper()
	gin.SetMode(gin.TestMode)
	s := &server{chat: chat.New(verify)}
	r := gin.New()
	r.GET("/ws", func(c *gin.Context) {
		s.mu.Lock()
		s.rawQuery = append(s.rawQuery, c.Request.URL.RawQuery)
		s.mu.Unlock()
	}, s.chat.Handler())
	if ln == nil {
		s.http = httptest.NewServer(r)
	} else {
		s.http = httptest.NewUnstartedServer(r)
		s.http.Listener.Close()
		s.http.Listener = ln
		s.http.Start()
	}
	s.url = "ws" + strings.TrimPrefix(s.http.URL, "http") + "/ws"
	t.Cleanup(func() { s.stop() })
	return s
}

func (s *server) stop() {
	s.http.CloseClientConnections()
	s.http.Close()
	s.chat.Close()
}

type recorder struct {
	mu     sync.Mutex
	states []State
	msgs   []Message
}

func (r *recorder) handler() Handler {
	return Handler{
		OnState:   func(s State, _ error) { r.mu.Lock(); r.states = append(r.states, s); r.mu.Unlock() },
		OnMessage: func(m Message) { r.mu.Lock(); r.msgs = append(r.msgs, m); r.mu.Unlock() },
	}
}

func (r *recorder) count(s State) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, x := range r.states {
		if x == s {
			n++
		}
	}
	return n
}

func (r *recorder) has(typ, text string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, m := range r.msgs {
		if m.Type == typ && strings.Contains(m.Text, text) {
			return true
		}
	}
	return false
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func start(t *testing.T, url, token string, tune func(*Config)) (*Client, *recorder, func()) {
	t.Helper()
	r := &recorder{}
	cfg := Config{URL: url, Token: func() string { return token }, MinBackoff: 20 * time.Millisecond, MaxBackoff: 50 * time.Millisecond}
	if tune != nil {
		tune(&cfg)
	}
	c := New(cfg, r.handler())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()
	stop := func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("Run did not return")
		}
	}
	t.Cleanup(stop)
	return c, r, stop
}

func TestConnectSayAndTokenInHeader(t *testing.T) {
	s := startServer(t, nil)
	alice, ra, _ := start(t, s.url, "alice", nil)
	_, rb, _ := start(t, s.url, "bob", nil)
	waitUntil(t, "both in general", func() bool { return s.chat.Members("general") == 2 && ra.count(Connected) == 1 })

	if err := alice.Say("hello bob"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "bob got it", func() bool { return rb.has("chat", "hello bob") })
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, q := range s.rawQuery {
		if strings.Contains(q, "token") {
			t.Fatalf("token sent in the URL (%q); it belongs in the Authorization header", q)
		}
	}
}

func TestJoinOtherRoomIsIsolated(t *testing.T) {
	s := startServer(t, nil)
	alice, _, _ := start(t, s.url, "alice", nil)
	charlie, rc, _ := start(t, s.url, "charlie", nil)
	waitUntil(t, "connected", func() bool { return s.chat.Members("general") == 2 })

	charlie.Join("one-piece")
	waitUntil(t, "charlie moved", func() bool { return s.chat.Members("one-piece") == 1 })
	if charlie.Room() != "one-piece" {
		t.Fatalf("Room() = %q", charlie.Room())
	}
	alice.Say("general news")
	charlie.Say("one-piece news")
	waitUntil(t, "charlie got his own", func() bool { return rc.has("chat", "one-piece news") })
	if rc.has("chat", "general news") {
		t.Fatal("charlie received a general message")
	}
}

// After a reconnect the client is back in the room the user picked.
func TestReconnectRejoinsRoom(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	s := startServer(t, ln)
	c, r, _ := start(t, s.url, "charlie", nil)
	waitUntil(t, "connected", func() bool { return r.count(Connected) == 1 })
	c.Join("naruto")
	waitUntil(t, "in naruto", func() bool { return s.chat.Members("naruto") == 1 })

	s.stop() // backend restart
	waitUntil(t, "disconnected", func() bool { return r.count(Disconnected) >= 1 })
	if err := c.Say("lost?"); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("Say while offline = %v, want ErrNotConnected", err)
	}

	ln2, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	s2 := startServer(t, ln2)
	waitUntil(t, "back in naruto", func() bool { return r.count(Connected) == 2 && s2.chat.Members("naruto") == 1 })
	if s2.chat.Members("general") != 0 {
		t.Fatal("client stayed in general after reconnect")
	}
}

func TestRejectedTokenStops(t *testing.T) {
	s := startServer(t, nil)
	_, r, _ := start(t, s.url, "mallory", nil)
	waitUntil(t, "rejected", func() bool { return r.count(Rejected) == 1 })
	time.Sleep(100 * time.Millisecond)
	if r.count(Connecting) != 1 {
		t.Fatalf("retried a rejected token %d times", r.count(Connecting)-1)
	}
}

func TestCancelLeavesRoom(t *testing.T) {
	s := startServer(t, nil)
	_, ra, _ := start(t, s.url, "alice", nil)
	_, _, stopBob := start(t, s.url, "bob", nil)
	waitUntil(t, "two", func() bool { return s.chat.Members("general") == 2 })
	stopBob()
	waitUntil(t, "bob left", func() bool { return ra.has("system", "bob left") && s.chat.Members("general") == 1 })
}

func TestServerDownKeepsRetrying(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	url := "ws://" + ln.Addr().String() + "/ws"
	ln.Close()
	_, r, _ := start(t, url, "alice", nil)
	waitUntil(t, "retries", func() bool { return r.count(Disconnected) >= 3 })
	if r.count(Rejected) != 0 {
		t.Fatal("connection refused must not count as rejected")
	}
}

// A server that stops sending anything (no pings) is detected after ReadTimeout.
func TestSilentServerDetected(t *testing.T) {
	s := startServer(t, nil) // its pings come every 50 s, far beyond our timeout
	_, r, _ := start(t, s.url, "alice", func(c *Config) { c.ReadTimeout = 150 * time.Millisecond })
	waitUntil(t, "reconnected after silence", func() bool { return r.count(Disconnected) >= 1 && r.count(Connected) >= 2 })
}

func TestBadURL(t *testing.T) {
	_, r, _ := start(t, "http://not-a-ws-url", "alice", nil)
	waitUntil(t, "disconnected", func() bool { return r.count(Disconnected) >= 1 })
	_ = http.StatusOK
}
