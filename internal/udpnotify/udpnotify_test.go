package udpnotify

import (
	"encoding/json"
	"errors"
	"net"
	"sync"
	"testing"
	"time"
)

func fakeVerify(tok string) (int64, error) {
	switch tok {
	case "alice":
		return 1, nil
	case "bob":
		return 2, nil
	case "charlie":
		return 3, nil
	}
	return 0, errors.New("invalid token")
}

func start(t *testing.T) (*Notifier, *net.UDPAddr) {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	n := New(fakeVerify, time.Minute)
	go n.Serve(conn)
	t.Cleanup(func() { n.Close() })
	return n, conn.LocalAddr().(*net.UDPAddr)
}

type udpClient struct {
	conn   *net.UDPConn
	server *net.UDPAddr
}

func newClient(t *testing.T, server *net.UDPAddr) *udpClient {
	t.Helper()
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return &udpClient{conn: c, server: server}
}

func (c *udpClient) send(m Msg) {
	b, _ := json.Marshal(m)
	c.conn.WriteToUDP(b, c.server)
}

func (c *udpClient) read(d time.Duration) (Msg, bool) {
	buf := make([]byte, 2048)
	c.conn.SetReadDeadline(time.Now().Add(d))
	n, _, err := c.conn.ReadFromUDP(buf)
	if err != nil {
		return Msg{}, false
	}
	var m Msg
	json.Unmarshal(buf[:n], &m)
	return m, true
}

func (c *udpClient) subscribe(t *testing.T, token string) {
	t.Helper()
	c.send(Msg{Type: "subscribe", Token: token})
	if m, ok := c.read(time.Second); !ok || m.Type != "subscribed" {
		t.Fatalf("subscribe %s: %+v ok=%v", token, m, ok)
	}
}

func TestTwoSubscribedOneNot(t *testing.T) {
	n, addr := start(t)
	alice, bob, charlie := newClient(t, addr), newClient(t, addr), newClient(t, addr)
	alice.subscribe(t, "alice")
	bob.subscribe(t, "bob")
	_ = charlie // never subscribes

	if sent := n.Broadcast("one-piece", "One Piece", 1131); sent != 2 {
		t.Fatalf("sent = %d, want 2", sent)
	}
	for name, c := range map[string]*udpClient{"alice": alice, "bob": bob} {
		m, ok := c.read(time.Second)
		if !ok || m.Type != "new_chapter" || m.MangaID != "one-piece" || m.TotalChapters != 1131 {
			t.Fatalf("%s got %+v ok=%v", name, m, ok)
		}
	}
	if m, ok := charlie.read(200 * time.Millisecond); ok {
		t.Fatalf("unsubscribed client received %+v", m)
	}
}

func TestInvalidTokenRejected(t *testing.T) {
	n, addr := start(t)
	c := newClient(t, addr)
	c.send(Msg{Type: "subscribe", Token: "forged"})
	if m, ok := c.read(time.Second); !ok || m.Type != "error" {
		t.Fatalf("got %+v ok=%v", m, ok)
	}
	if n.Subscribers() != 0 {
		t.Fatal("forged token was registered")
	}
}

func TestBadJSON(t *testing.T) {
	_, addr := start(t)
	c := newClient(t, addr)
	c.conn.WriteToUDP([]byte("not json"), addr)
	if m, ok := c.read(time.Second); !ok || m.Type != "error" {
		t.Fatalf("got %+v ok=%v", m, ok)
	}
}

func TestUnsubscribe(t *testing.T) {
	n, addr := start(t)
	c := newClient(t, addr)
	c.subscribe(t, "alice")
	c.send(Msg{Type: "unsubscribe", Token: "alice"})
	deadline := time.Now().Add(time.Second)
	for n.Subscribers() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n.Subscribers() != 0 {
		t.Fatal("still subscribed")
	}
	n.Broadcast("x", "X", 1)
	if m, ok := c.read(200 * time.Millisecond); ok {
		t.Fatalf("unsubscribed client got %+v", m)
	}
}

// A subscriber whose socket is gone must not crash or block the server.
func TestDeadReceiverDoesNotCrash(t *testing.T) {
	n, addr := start(t)
	dead := newClient(t, addr)
	dead.subscribe(t, "alice")
	dead.conn.Close() // port closed → ICMP "connection refused" on Linux

	live := newClient(t, addr)
	live.subscribe(t, "bob")
	for i := 0; i < 3; i++ {
		n.Broadcast("one-piece", "One Piece", 1131+i)
	}
	// Server must still answer a new subscriber after the errors.
	late := newClient(t, addr)
	late.subscribe(t, "charlie")
	if _, ok := live.read(time.Second); !ok {
		t.Fatal("live subscriber got nothing")
	}
}

func TestExpireSilentSubscriber(t *testing.T) {
	n, addr := start(t)
	var mu sync.Mutex
	fake := time.Now()
	n.mu.Lock()
	n.now = func() time.Time { mu.Lock(); defer mu.Unlock(); return fake }
	n.mu.Unlock()

	c := newClient(t, addr)
	c.subscribe(t, "alice")
	mu.Lock()
	fake = fake.Add(2 * time.Minute) // > ttl (1 min) with no heartbeat
	mu.Unlock()
	if sent := n.Broadcast("x", "X", 1); sent != 0 {
		t.Fatalf("expired subscriber still notified (sent=%d)", sent)
	}
}
