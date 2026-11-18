package tcpclient

import (
	"bufio"
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mangahub/internal/tcpsync"
	"mangahub/internal/tui/client"
)

// Tests run against member A's real tcpsync server (imported, not changed).

var users = map[string]int64{"tok-alice": 1, "tok-bob": 2}

func verify(tok string) (int64, error) {
	if uid, ok := users[tok]; ok {
		return uid, nil
	}
	return 0, errors.New("bad token")
}

type server struct {
	hub  *tcpsync.Hub
	srv  *tcpsync.Server
	addr string
}

func startServer(t *testing.T, addr string) *server {
	t.Helper()
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	hub := tcpsync.NewHub()
	srv := tcpsync.NewServer(hub, verify)
	go srv.Serve(ln)
	s := &server{hub: hub, srv: srv, addr: ln.Addr().String()}
	t.Cleanup(func() { srv.Close() })
	return s
}

// recorder collects callbacks and lets the test wait for them.
type recorder struct {
	mu       sync.Mutex
	states   []State
	sessions []string
	progress []client.Progress
	lastErr  error
}

func (r *recorder) handler() Handler {
	return Handler{
		OnState: func(s State, sid string, err error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.states = append(r.states, s)
			if s == Connected {
				r.sessions = append(r.sessions, sid)
			}
			if err != nil {
				r.lastErr = err
			}
		},
		OnProgress: func(p client.Progress) {
			r.mu.Lock()
			r.progress = append(r.progress, p)
			r.mu.Unlock()
		},
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

func (r *recorder) lastSession() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.sessions) == 0 {
		return ""
	}
	return r.sessions[len(r.sessions)-1]
}

func (r *recorder) events() []client.Progress {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]client.Progress(nil), r.progress...)
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

// run starts Run in the background and returns its recorder and a stop func.
func run(t *testing.T, cfg Config) (*recorder, func()) {
	t.Helper()
	r := &recorder{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { Run(ctx, cfg, r.handler()); close(done) }()
	stop := func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("Run did not return after cancel")
		}
	}
	t.Cleanup(stop)
	return r, stop
}

func tok(s string) func() string { return func() string { return s } }

func TestConnectAndReceiveProgress(t *testing.T) {
	s := startServer(t, "")
	r, _ := run(t, Config{Addr: s.addr, Token: tok("tok-alice")})
	waitUntil(t, "connected", func() bool { return r.lastSession() != "" })
	waitUntil(t, "registered in hub", func() bool { return s.hub.Sessions(1) == 1 })

	n := s.hub.SendToUser(1, tcpsync.ProgressEvent("one-piece", 26, "reading", 4), "")
	if n != 1 {
		t.Fatalf("hub delivered to %d sessions", n)
	}
	waitUntil(t, "event", func() bool { return len(r.events()) == 1 })
	if p := r.events()[0]; p != (client.Progress{MangaID: "one-piece", CurrentChapter: 26, Status: "reading", Version: 4}) {
		t.Fatalf("progress = %+v", p)
	}
	// Chapter 0 must survive the trip (it is a pointer on the wire).
	s.hub.SendToUser(1, tcpsync.ProgressEvent("naruto", 0, "plan-to-read", 2), "")
	waitUntil(t, "chapter 0 event", func() bool { return len(r.events()) == 2 })
	if p := r.events()[1]; p.CurrentChapter != 0 || p.MangaID != "naruto" {
		t.Fatalf("progress = %+v", p)
	}
}

// F03 rule: the session that made the change is skipped, other sessions of
// the same user get the event, other users never do.
func TestOnlyOtherSessionsOfSameUser(t *testing.T) {
	s := startServer(t, "")
	a1, _ := run(t, Config{Addr: s.addr, Token: tok("tok-alice")})
	a2, _ := run(t, Config{Addr: s.addr, Token: tok("tok-alice")})
	bob, _ := run(t, Config{Addr: s.addr, Token: tok("tok-bob")})
	waitUntil(t, "all connected", func() bool {
		return a1.lastSession() != "" && a2.lastSession() != "" && bob.lastSession() != "" &&
			s.hub.Sessions(1) == 2 && s.hub.Sessions(2) == 1
	})

	// a1 made the HTTP change (its session id goes in X-Session-ID).
	if n := s.hub.SendToUser(1, tcpsync.ProgressEvent("one-piece", 30, "reading", 5), a1.lastSession()); n != 1 {
		t.Fatalf("delivered to %d sessions, want 1", n)
	}
	waitUntil(t, "a2 got it", func() bool { return len(a2.events()) == 1 })
	time.Sleep(100 * time.Millisecond)
	if len(a1.events()) != 0 || len(bob.events()) != 0 {
		t.Fatalf("a1 got %d, bob got %d; want 0 and 0", len(a1.events()), len(bob.events()))
	}
}

func TestBadTokenStopsWithoutRetrying(t *testing.T) {
	s := startServer(t, "")
	r, _ := run(t, Config{Addr: s.addr, Token: tok("forged"), MinBackoff: 10 * time.Millisecond})
	waitUntil(t, "auth failed", func() bool { return r.count(AuthFailed) == 1 })
	time.Sleep(100 * time.Millisecond)
	if r.count(Connecting) != 1 {
		t.Fatalf("connected %d times; a rejected token must not be retried", r.count(Connecting))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !errors.Is(r.lastErr, ErrAuth) {
		t.Fatalf("err = %v, want ErrAuth", r.lastErr)
	}
}

func TestReconnectsAfterServerRestart(t *testing.T) {
	s := startServer(t, "")
	r, _ := run(t, Config{Addr: s.addr, Token: tok("tok-alice"), MinBackoff: 20 * time.Millisecond, MaxBackoff: 50 * time.Millisecond})
	waitUntil(t, "connected", func() bool { return r.lastSession() != "" })
	first := r.lastSession()

	s.srv.Close() // backend goes down
	waitUntil(t, "disconnected", func() bool { return r.count(Disconnected) >= 1 })

	s2 := startServer(t, s.addr) // same address, new process state
	waitUntil(t, "reconnected", func() bool { return r.count(Connected) == 2 && s2.hub.Sessions(1) == 1 })
	if r.lastSession() == "" {
		t.Fatal("no session id after reconnect")
	}
	t.Logf("session %s -> %s after restart", first, r.lastSession())
	s2.hub.SendToUser(1, tcpsync.ProgressEvent("one-piece", 31, "reading", 6), "")
	waitUntil(t, "event after reconnect", func() bool { return len(r.events()) == 1 })
}

func TestBackendNotRunningKeepsRetrying(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close() // nothing listens here
	r, _ := run(t, Config{Addr: addr, Token: tok("tok-alice"), MinBackoff: 10 * time.Millisecond, MaxBackoff: 20 * time.Millisecond})
	waitUntil(t, "several attempts", func() bool { return r.count(Disconnected) >= 3 })
	if r.count(AuthFailed) != 0 || r.count(Connected) != 0 {
		t.Fatal("unexpected state")
	}
}

func TestCancelClosesSession(t *testing.T) {
	s := startServer(t, "")
	r, stop := run(t, Config{Addr: s.addr, Token: tok("tok-alice")})
	waitUntil(t, "registered", func() bool { return s.hub.Sessions(1) == 1 })
	stop()
	if r.count(Stopped) != 1 {
		t.Fatalf("states = %v, want a final Stopped", r.states)
	}
	waitUntil(t, "server removed the session", func() bool { return s.hub.Sessions(1) == 0 })
}

// Pings keep a quiet connection alive: with a short interval the server's
// pongs arrive well inside ReadTimeout and there is no reconnect.
func TestPingKeepsIdleSessionAlive(t *testing.T) {
	s := startServer(t, "")
	r, _ := run(t, Config{Addr: s.addr, Token: tok("tok-alice"), PingInterval: 30 * time.Millisecond, ReadTimeout: 100 * time.Millisecond})
	waitUntil(t, "connected", func() bool { return r.lastSession() != "" })
	time.Sleep(400 * time.Millisecond) // 4× ReadTimeout with no events
	if r.count(Disconnected) != 0 {
		t.Fatalf("dropped an idle but healthy session %d time(s)", r.count(Disconnected))
	}
}

// A server that accepts auth and then goes silent (frozen process, cable
// pulled): the client must notice after ReadTimeout and reconnect.
func TestSilentServerIsDetected(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var accepted atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			go func(c net.Conn) {
				defer c.Close()
				br := bufio.NewReader(c)
				br.ReadString('\n')                                           // auth line
				c.Write([]byte(`{"type":"auth_ok","session_id":"x"}` + "\n")) // then never answer pings
				time.Sleep(2 * time.Second)
			}(c)
		}
	}()
	r, _ := run(t, Config{Addr: ln.Addr().String(), Token: tok("t"), PingInterval: 20 * time.Millisecond,
		ReadTimeout: 80 * time.Millisecond, MinBackoff: 10 * time.Millisecond})
	waitUntil(t, "detected and reconnected", func() bool { return r.count(Disconnected) >= 1 && accepted.Load() >= 2 })
}

func TestMalformedLineIsSkipped(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		bufio.NewReader(c).ReadString('\n')
		c.Write([]byte(`{"type":"auth_ok","session_id":"s1"}` + "\n" +
			"this is not json\n" +
			`{"type":"progress_updated","manga_id":"a","current_chapter":3,"status":"reading","version":2}` + "\n"))
		time.Sleep(time.Second)
	}()
	r, _ := run(t, Config{Addr: ln.Addr().String(), Token: tok("t")})
	waitUntil(t, "event after bad line", func() bool { return len(r.events()) == 1 })
	if r.count(Disconnected) != 0 {
		t.Fatal("a malformed line must not drop the session")
	}
}

// Two events sent back to back in one TCP segment are split by '\n', and an
// event split across two writes is joined: TCP has no message boundaries.
func TestFramingAcrossWrites(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		bufio.NewReader(c).ReadString('\n')
		c.Write([]byte(`{"type":"auth_ok","session_id":"s1"}` + "\n" +
			`{"type":"progress_updated","manga_id":"a","current_chapter":1,"version":2}` + "\n" +
			`{"type":"progress_updated","manga_id":"b","curr`))
		time.Sleep(50 * time.Millisecond)
		c.Write([]byte(`ent_chapter":2,"version":3}` + "\n"))
		time.Sleep(time.Second)
	}()
	r, _ := run(t, Config{Addr: ln.Addr().String(), Token: tok("t")})
	waitUntil(t, "two events", func() bool { return len(r.events()) == 2 })
	ev := r.events()
	if ev[0].MangaID != "a" || ev[1].MangaID != "b" || ev[1].CurrentChapter != 2 {
		t.Fatalf("events = %+v", ev)
	}
}
