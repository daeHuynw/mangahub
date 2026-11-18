package udpclient

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mangahub/internal/udpnotify"
)

// Tests use member A's real udpnotify.Notifier (imported, not changed).

func verify(tok string) (int64, error) {
	switch tok {
	case "tok-alice":
		return 1, nil
	case "tok-bob":
		return 2, nil
	}
	return 0, errors.New("bad token")
}

type notifier struct {
	n    *udpnotify.Notifier
	addr string
}

func startNotifier(t *testing.T, addr string, ttl time.Duration) *notifier {
	t.Helper()
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	ua, _ := net.ResolveUDPAddr("udp", addr)
	conn, err := net.ListenUDP("udp", ua)
	if err != nil {
		t.Fatal(err)
	}
	n := udpnotify.New(verify, ttl)
	go n.Serve(conn)
	t.Cleanup(func() { n.Close() })
	return &notifier{n: n, addr: conn.LocalAddr().String()}
}

type recorder struct {
	mu       sync.Mutex
	states   []State
	chapters []Chapter
}

func (r *recorder) handler() Handler {
	return Handler{
		OnState: func(s State, _ error) { r.mu.Lock(); r.states = append(r.states, s); r.mu.Unlock() },
		OnChapter: func(c Chapter) {
			r.mu.Lock()
			r.chapters = append(r.chapters, c)
			r.mu.Unlock()
		},
	}
}

func (r *recorder) has(s State) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, x := range r.states {
		if x == s {
			return true
		}
	}
	return false
}

func (r *recorder) last() State {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.states) == 0 {
		return -1
	}
	return r.states[len(r.states)-1]
}

func (r *recorder) got() []Chapter {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Chapter(nil), r.chapters...)
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

func run(t *testing.T, cfg Config) (*recorder, func()) {
	t.Helper()
	r := &recorder{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		if err := Run(ctx, cfg, r.handler()); err != nil {
			t.Errorf("Run: %v", err)
		}
		close(done)
	}()
	stop := func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("Run did not return")
		}
	}
	t.Cleanup(stop)
	return r, stop
}

func tok(s string) func() string { return func() string { return s } }

func TestSubscribeAndReceive(t *testing.T) {
	ns := startNotifier(t, "", 0)
	r, _ := run(t, Config{Addr: ns.addr, Token: tok("tok-alice")})
	waitUntil(t, "subscribed", func() bool { return r.last() == Subscribed && ns.n.Subscribers() == 1 })

	if sent := ns.n.Broadcast("one-piece", "One Piece", 1131); sent != 1 {
		t.Fatalf("broadcast sent %d", sent)
	}
	waitUntil(t, "alert", func() bool { return len(r.got()) == 1 })
	if c := r.got()[0]; c != (Chapter{MangaID: "one-piece", Title: "One Piece", TotalChapters: 1131}) {
		t.Fatalf("chapter = %+v", c)
	}
}

// F04 acceptance: two subscribed clients receive, a client that never
// subscribed (just an open UDP socket) does not.
func TestTwoSubscribedOneNot(t *testing.T) {
	ns := startNotifier(t, "", 0)
	a, _ := run(t, Config{Addr: ns.addr, Token: tok("tok-alice")})
	b, _ := run(t, Config{Addr: ns.addr, Token: tok("tok-bob")})
	ra, _ := net.ResolveUDPAddr("udp", ns.addr)
	silent, err := net.DialUDP("udp", nil, ra) // never sends subscribe
	if err != nil {
		t.Fatal(err)
	}
	defer silent.Close()
	waitUntil(t, "two subscribers", func() bool { return a.last() == Subscribed && b.last() == Subscribed && ns.n.Subscribers() == 2 })

	if sent := ns.n.Broadcast("naruto", "Naruto", 701); sent != 2 {
		t.Fatalf("broadcast sent %d, want 2", sent)
	}
	waitUntil(t, "both got it", func() bool { return len(a.got()) == 1 && len(b.got()) == 1 })
	silent.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	if n, err := silent.Read(make([]byte, 2048)); err == nil {
		t.Fatalf("unsubscribed socket received %d bytes", n)
	}
}

func TestCancelSendsUnsubscribe(t *testing.T) {
	ns := startNotifier(t, "", 0)
	r, stop := run(t, Config{Addr: ns.addr, Token: tok("tok-alice")})
	waitUntil(t, "subscribed", func() bool { return ns.n.Subscribers() == 1 })
	stop()
	if r.last() != Stopped {
		t.Fatalf("last state %v", r.last())
	}
	waitUntil(t, "server forgot us", func() bool { return ns.n.Subscribers() == 0 })
}

// On logout the app clears its token right after cancelling Run. The
// unsubscribe must still carry the token that subscribed (regression: it
// used to read the cleared token and the server rejected it).
func TestUnsubscribeAfterTokenCleared(t *testing.T) {
	ns := startNotifier(t, "", 0)
	var cleared atomic.Bool
	token := func() string {
		if cleared.Load() {
			return ""
		}
		return "tok-alice"
	}
	_, stop := run(t, Config{Addr: ns.addr, Token: token})
	waitUntil(t, "subscribed", func() bool { return ns.n.Subscribers() == 1 })
	cleared.Store(true) // what App.endSession does via api.Logout()
	stop()
	waitUntil(t, "unsubscribed with the old token", func() bool { return ns.n.Subscribers() == 0 })
}

func TestBadTokenRejected(t *testing.T) {
	ns := startNotifier(t, "", 0)
	r, _ := run(t, Config{Addr: ns.addr, Token: tok("forged")})
	waitUntil(t, "rejected", func() bool { return r.last() == Rejected })
	if ns.n.Subscribers() != 0 {
		t.Fatal("forged token got subscribed")
	}
}

// Server not running yet: 3 tries, then NoAck. When it starts, the next
// heartbeat subscribes. On Windows this also proves the "connection reset"
// read errors (ICMP port unreachable) do not kill the reader.
func TestServerDownThenUp(t *testing.T) {
	probe, _ := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	addr := probe.LocalAddr().String()
	probe.Close()

	r, _ := run(t, Config{Addr: addr, Token: tok("tok-alice"), AckTimeout: 30 * time.Millisecond, Heartbeat: 200 * time.Millisecond})
	waitUntil(t, "no ack", func() bool { return r.last() == NoAck })

	ns := startNotifier(t, addr, 0)
	waitUntil(t, "subscribed after server start", func() bool { return r.last() == Subscribed && ns.n.Subscribers() == 1 })
	ns.n.Broadcast("bleach", "Bleach", 687)
	waitUntil(t, "alert", func() bool { return len(r.got()) == 1 })
}

// The first two acks are "lost" (a proxy drops them): the client retries and
// is subscribed on the third try.
func TestLostAcksAreRetried(t *testing.T) {
	srv, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	var subs atomic.Int32
	go func() {
		buf := make([]byte, 2048)
		for {
			n, from, err := srv.ReadFromUDP(buf)
			if err != nil {
				return
			}
			var m msg
			json.Unmarshal(buf[:n], &m)
			if m.Type == "subscribe" && subs.Add(1) >= 3 {
				srv.WriteToUDP([]byte(`{"type":"subscribed"}`), from)
			}
		}
	}()
	r, _ := run(t, Config{Addr: srv.LocalAddr().String(), Token: tok("t"), AckTimeout: 30 * time.Millisecond, Heartbeat: time.Hour})
	waitUntil(t, "subscribed on 3rd try", func() bool { return r.last() == Subscribed })
	if n := subs.Load(); n != 3 {
		t.Fatalf("sent %d subscribes, want 3", n)
	}
	if r.has(NoAck) {
		t.Fatal("NoAck reported although the 3rd try succeeded")
	}
}

// Heartbeats keep the subscription past the server's TTL.
func TestHeartbeatKeepsSubscription(t *testing.T) {
	ns := startNotifier(t, "", 150*time.Millisecond) // short TTL
	r, _ := run(t, Config{Addr: ns.addr, Token: tok("tok-alice"), Heartbeat: 40 * time.Millisecond})
	waitUntil(t, "subscribed", func() bool { return r.last() == Subscribed })
	time.Sleep(500 * time.Millisecond) // > 3x TTL
	if sent := ns.n.Broadcast("akira", "Akira", 121); sent != 1 {
		t.Fatalf("subscription expired despite heartbeats (sent=%d)", sent)
	}
	waitUntil(t, "alert", func() bool { return len(r.got()) == 1 })
}

func TestMalformedDatagramIgnored(t *testing.T) {
	srv, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	go func() {
		buf := make([]byte, 2048)
		_, from, err := srv.ReadFromUDP(buf)
		if err != nil {
			return
		}
		srv.WriteToUDP([]byte(`not json`), from)
		srv.WriteToUDP([]byte(`{"type":"subscribed"}`), from)
		srv.WriteToUDP([]byte(`{"type":"new_chapter","manga_id":"x","title":"X","total_chapters":2}`), from)
	}()
	r, _ := run(t, Config{Addr: srv.LocalAddr().String(), Token: tok("t"), Heartbeat: time.Hour})
	waitUntil(t, "alert after junk", func() bool { return r.last() == Subscribed && len(r.got()) == 1 })
}

func TestBadAddress(t *testing.T) {
	if err := Run(context.Background(), Config{Addr: "no-port"}, Handler{}); err == nil {
		t.Fatal("want error for an address without port")
	}
}
