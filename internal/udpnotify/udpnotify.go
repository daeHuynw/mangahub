// Package udpnotify delivers best-effort new-chapter alerts (feature F04).
//
// One JSON object per datagram. A client registers its return address with
// {"type":"subscribe","token":...} and must repeat it (heartbeat) at least
// every TTL, otherwise the address is forgotten. Delivery is NOT guaranteed:
// a lost datagram is simply lost.
package udpnotify

import (
	"encoding/json"
	"errors"
	"log"
	"net"
	"sync"
	"time"
)

const (
	DefaultTTL  = 90 * time.Second
	MaxDatagram = 1200 // stay under a typical MTU
)

type Msg struct {
	Type          string `json:"type"`
	Token         string `json:"token,omitempty"`
	MangaID       string `json:"manga_id,omitempty"`
	Title         string `json:"title,omitempty"`
	TotalChapters int    `json:"total_chapters,omitempty"`
	Error         string `json:"error,omitempty"`
}

// Verifier turns a JWT into a user id.
type Verifier func(token string) (uid int64, err error)

type subscriber struct {
	addr     *net.UDPAddr
	uid      int64
	lastSeen time.Time
}

type Notifier struct {
	verify Verifier
	ttl    time.Duration
	now    func() time.Time

	conn *net.UDPConn
	mu   sync.Mutex
	subs map[string]*subscriber // key = addr.String()
	done chan struct{}
	once sync.Once
}

func New(verify Verifier, ttl time.Duration) *Notifier {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &Notifier{verify: verify, ttl: ttl, now: time.Now, subs: map[string]*subscriber{}, done: make(chan struct{})}
}

// ListenAndServe binds addr and serves until Close.
func (n *Notifier) ListenAndServe(addr string) error {
	ua, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return err
	}
	conn, err := net.ListenUDP("udp", ua)
	if err != nil {
		return err
	}
	return n.Serve(conn)
}

// Serve reads subscribe/unsubscribe datagrams from conn until Close.
func (n *Notifier) Serve(conn *net.UDPConn) error {
	n.mu.Lock()
	n.conn = conn
	n.mu.Unlock()
	log.Printf("udpnotify: listening on %s", conn.LocalAddr())
	go n.expireLoop()

	buf := make([]byte, 2048)
	for {
		size, addr, err := conn.ReadFromUDP(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			// On Linux a previous send to a closed client port can surface here
			// as "connection refused" (ICMP). It must not stop the server.
			log.Printf("udpnotify: read: %v", err)
			continue
		}
		n.handle(addr, buf[:size])
	}
}

func (n *Notifier) handle(addr *net.UDPAddr, data []byte) {
	var m Msg
	if err := json.Unmarshal(data, &m); err != nil {
		n.send(addr, Msg{Type: "error", Error: "bad json"})
		return
	}
	if m.Type != "subscribe" && m.Type != "unsubscribe" {
		n.send(addr, Msg{Type: "error", Error: "unknown type"})
		return
	}
	uid, err := n.verify(m.Token)
	if err != nil {
		n.send(addr, Msg{Type: "error", Error: "invalid token"})
		return
	}
	key := addr.String()
	n.mu.Lock()
	switch m.Type {
	case "subscribe":
		if _, existed := n.subs[key]; !existed {
			log.Printf("udpnotify: subscribe user %d from %s", uid, key)
		}
		n.subs[key] = &subscriber{addr: addr, uid: uid, lastSeen: n.now()}
	case "unsubscribe":
		if s, ok := n.subs[key]; ok && s.uid == uid {
			delete(n.subs, key)
			log.Printf("udpnotify: unsubscribe %s", key)
		}
	}
	n.mu.Unlock()
	if m.Type == "subscribe" {
		n.send(addr, Msg{Type: "subscribed"})
	}
}

func (n *Notifier) send(addr *net.UDPAddr, m Msg) error {
	b, _ := json.Marshal(m)
	n.mu.Lock()
	conn := n.conn
	n.mu.Unlock()
	if conn == nil {
		return net.ErrClosed
	}
	conn.SetWriteDeadline(time.Now().Add(time.Second))
	_, err := conn.WriteToUDP(b, addr)
	if err != nil {
		log.Printf("udpnotify: write to %s: %v", addr, err) // log only, never exit
	}
	return err
}

// Broadcast sends new_chapter to every live subscriber and returns how many
// datagrams were handed to the OS. That is not proof of delivery (UDP).
func (n *Notifier) Broadcast(mangaID, title string, totalChapters int) int {
	n.pruneExpired()
	n.mu.Lock()
	targets := make([]*net.UDPAddr, 0, len(n.subs))
	for _, s := range n.subs {
		targets = append(targets, s.addr) // copy, then release the lock before I/O
	}
	n.mu.Unlock()

	sent := 0
	msg := Msg{Type: "new_chapter", MangaID: mangaID, Title: title, TotalChapters: totalChapters}
	for _, a := range targets {
		if n.send(a, msg) == nil {
			sent++
		}
	}
	log.Printf("udpnotify: new_chapter %s=%d sent to %d/%d subscriber(s)", mangaID, totalChapters, sent, len(targets))
	return sent
}

// Subscribers returns the current number of registered addresses.
func (n *Notifier) Subscribers() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.subs)
}

func (n *Notifier) pruneExpired() {
	n.mu.Lock()
	defer n.mu.Unlock()
	for k, s := range n.subs {
		if n.now().Sub(s.lastSeen) > n.ttl {
			delete(n.subs, k)
			log.Printf("udpnotify: expired %s", k)
		}
	}
}

func (n *Notifier) expireLoop() {
	t := time.NewTicker(n.ttl / 3)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			n.pruneExpired()
		case <-n.done:
			return
		}
	}
}

func (n *Notifier) Close() error {
	n.once.Do(func() { close(n.done) })
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.conn != nil {
		return n.conn.Close()
	}
	return nil
}
