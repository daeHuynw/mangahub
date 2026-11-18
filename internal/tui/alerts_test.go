package tui

import (
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"mangahub/internal/tui/udpclient"
	"mangahub/internal/udpnotify"
)

// startNotifier runs member A's real UDP notifier; token "tok" is user 1.
func startNotifier(t *testing.T) (*udpnotify.Notifier, string) {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	n := udpnotify.New(func(tok string) (int64, error) {
		if strings.HasPrefix(tok, "tok:") {
			return 1, nil
		}
		return 0, errors.New("bad token")
	}, 0)
	go n.Serve(conn)
	t.Cleanup(func() { n.Close() })
	return n, conn.LocalAddr().String()
}

func TestUI_NewChapterAlert(t *testing.T) {
	n, addr := startNotifier(t)
	h := startWith(t, func(h *harness) { h.udpAddr = addr })
	h.login("alice", "secret123")
	h.waitFor("subscribed", func() bool {
		return h.a.alerts.state == udpclient.Subscribed && strings.Contains(h.a.header.GetText(true), "alerts on")
	})
	if n.Subscribers() != 1 {
		t.Fatalf("notifier has %d subscribers", n.Subscribers())
	}

	// One Piece is selected in the browser and in the library.
	h.waitFor("catalog", func() bool { return h.a.browser.list.GetItemCount() == 3 })
	h.typeText("a")
	h.waitFor("added", func() bool { return strings.Contains(h.a.status.GetText(true), "Added One Piece") })
	h.key(tcell.KeyF3)
	h.waitFor("row", func() bool { return h.cell(1, 2) == "0 / 1130" })

	if sent := n.Broadcast("one-piece", "One Piece", 1131); sent != 1 {
		t.Fatalf("broadcast sent %d", sent)
	}
	h.waitFor("alert shown everywhere", func() bool {
		label, _ := h.a.browser.list.GetItemText(0)
		return strings.Contains(h.a.status.GetText(true), "New chapter: One Piece now has 1131") &&
			strings.Contains(h.a.header.GetText(true), "(1 new)") &&
			h.cell(1, 2) == "0 / 1131" &&
			h.a.browser.shown[0].TotalChapters == 1131 &&
			strings.Contains(label, "1131 ch")
	})

	// F5 lists it and clears the unread counter.
	h.key(tcell.KeyF5)
	h.waitFor("notifications page", func() bool {
		c1, c2 := h.a.notifications.table.GetCell(1, 1), h.a.notifications.table.GetCell(1, 2)
		return c1 != nil && c1.Text == "One Piece" && c2.Text == "1131" &&
			!strings.Contains(h.a.header.GetText(true), "new)")
	})

	// While on F5, a second alert is added at the top and is not "unread".
	n.Broadcast("naruto", "Naruto", 701)
	h.waitFor("second alert on top", func() bool {
		c := h.a.notifications.table.GetCell(1, 1)
		return c != nil && c.Text == "Naruto" && h.a.notifications.unread == 0 && len(h.a.notifications.items) == 2
	})
}

// Ctrl+L logs out: the server forgets the address (unsubscribe) and later
// alerts do not reach this TUI.
func TestUI_LogoutUnsubscribes(t *testing.T) {
	n, addr := startNotifier(t)
	h := startWith(t, func(h *harness) { h.udpAddr = addr })
	h.login("alice", "secret123")
	h.waitFor("subscribed", func() bool { return h.a.alerts.state == udpclient.Subscribed })

	h.screen.InjectKey(tcell.KeyCtrlL, 0, tcell.ModCtrl)
	h.waitFor("logged out", func() bool {
		p, _ := h.a.pages.GetFrontPage()
		return p == pageLogin && !h.a.loggedIn && strings.Contains(h.a.login.message.GetText(true), "Logged out")
	})
	deadline := time.Now().Add(3 * time.Second)
	for n.Subscribers() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n.Subscribers() != 0 {
		t.Fatal("still subscribed after logout")
	}
	if sent := n.Broadcast("one-piece", "One Piece", 1131); sent != 0 {
		t.Fatalf("broadcast still sent to %d address(es)", sent)
	}
	time.Sleep(100 * time.Millisecond)
	var items int
	h.ui(func() { items = len(h.a.notifications.items) })
	if items != 0 {
		t.Fatalf("logged-out TUI recorded %d alert(s)", items)
	}
}

// Quitting the app (Ctrl+C) also unsubscribes before the process ends.
func TestUI_QuitUnsubscribes(t *testing.T) {
	n, addr := startNotifier(t)
	h := startWith(t, func(h *harness) { h.udpAddr = addr })
	h.login("alice", "secret123")
	h.waitFor("subscribed", func() bool { return h.a.alerts.state == udpclient.Subscribed })
	h.a.Stop() // same path as Ctrl+C: Run returns, then unsubscribe is sent
	deadline := time.Now().Add(3 * time.Second)
	for n.Subscribers() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n.Subscribers() != 0 {
		t.Fatal("still subscribed after quit")
	}
}
