package tui

import (
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"

	"mangahub/internal/tcpsync"
	"mangahub/internal/tui/client"
	"mangahub/internal/tui/tcpclient"
)

// syncServer is member A's real TCP sync server; token "tok" is user 1 (the
// fake HTTP backend hands out "tok" on login).
type syncServer struct {
	hub  *tcpsync.Hub
	srv  *tcpsync.Server
	addr string
}

func startSyncServer(t *testing.T, addr string) *syncServer {
	t.Helper()
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	hub := tcpsync.NewHub()
	srv := tcpsync.NewServer(hub, func(tok string) (int64, error) {
		if strings.HasPrefix(tok, "tok:") {
			return 1, nil
		}
		return 0, errors.New("bad token")
	})
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return &syncServer{hub: hub, srv: srv, addr: ln.Addr().String()}
}

func (h *harness) setFakeRow(id string, chapter int, version int64) {
	h.libMu.Lock()
	defer h.libMu.Unlock()
	h.lib[id].CurrentChapter, h.lib[id].Version = chapter, version
}

func TestUI_ProgressSyncOverTCP(t *testing.T) {
	ss := startSyncServer(t, "")
	h := startWith(t, func(h *harness) { h.tcpAddr = ss.addr })
	h.login("alice", "secret123")

	// Login opens the TCP session; the header shows it.
	h.waitFor("sync connected", func() bool {
		return h.a.sync.state == tcpclient.Connected && strings.Contains(h.a.header.GetText(true), "sync on")
	})
	var sid string
	h.ui(func() { sid = h.a.sync.sessionID })
	if ss.hub.Sessions(1) != 1 || sid == "" {
		t.Fatalf("hub sessions = %d, session id %q", ss.hub.Sessions(1), sid)
	}

	h.waitFor("catalog", func() bool { return h.a.browser.list.GetItemCount() == 3 })
	h.typeText("a")
	h.waitFor("added", func() bool { return strings.Contains(h.a.status.GetText(true), "Added One Piece") })
	h.key(tcell.KeyF3)
	h.waitFor("row v1", func() bool { return h.cell(1, 2) == "0 / 1130" && h.cell(1, 3) == "v1" })

	// Another session of alice saved chapter 42 (v2): the backend stores it
	// and pushes progress_updated to us. The row changes without any key.
	h.setFakeRow("one-piece", 42, 2)
	ss.hub.SendToUser(1, tcpsync.ProgressEvent("one-piece", 42, "reading", 2), "other-session")
	h.waitFor("pushed update shown", func() bool {
		return h.cell(1, 2) == "42 / 1130" && h.cell(1, 3) == "v2" &&
			strings.Contains(h.a.status.GetText(true), "Synced from another session")
	})

	// Our own change carries X-Session-ID = our TCP session.
	h.typeText("+")
	h.waitFor("own update", func() bool { return h.cell(1, 2) == "43 / 1130" && h.cell(1, 3) == "v3" })
	h.libMu.Lock()
	gotSID := h.lastSessionID
	h.libMu.Unlock()
	if gotSID != sid {
		t.Fatalf("PUT X-Session-ID = %q, want our session %q", gotSID, sid)
	}

	// A late/duplicate event (older version) must not move the row back.
	ss.hub.SendToUser(1, tcpsync.ProgressEvent("one-piece", 10, "reading", 2), "x")
	ss.hub.SendToUser(1, tcpsync.ProgressEvent("one-piece", 43, "reading", 3), "x")
	h.ui(func() {}) // let queued updates run
	h.waitFor("still v3", func() bool { return h.cell(1, 2) == "43 / 1130" && h.cell(1, 3) == "v3" })
}

// Events sent while the TCP link is down are lost; after reconnecting the
// TUI reloads the library over HTTP so it shows the current state.
func TestUI_ReconnectReloadsLibrary(t *testing.T) {
	ss := startSyncServer(t, "")
	h := startWith(t, func(h *harness) { h.tcpAddr = ss.addr })
	h.login("alice", "secret123")
	h.waitFor("catalog", func() bool { return h.a.browser.list.GetItemCount() == 3 })
	h.typeText("a")
	h.waitFor("added", func() bool { return strings.Contains(h.a.status.GetText(true), "Added") })
	h.key(tcell.KeyF3)
	h.waitFor("row v1", func() bool { return h.cell(1, 3) == "v1" && h.a.sync.state == tcpclient.Connected })

	ss.srv.Close() // backend's TCP side goes away
	h.waitFor("sync off", func() bool {
		return h.a.sync.state != tcpclient.Connected && strings.Contains(h.a.status.GetText(true), "Progress sync lost")
	})
	h.setFakeRow("one-piece", 50, 4) // changed elsewhere while we were offline

	startSyncServer(t, ss.addr) // back on the same port
	h.waitFor("reconnected and reloaded", func() bool {
		return h.a.sync.state == tcpclient.Connected && h.a.sync.connects == 2 &&
			h.cell(1, 2) == "50 / 1130" && h.cell(1, 3) == "v4"
	})
}

// An event for a manga added in the other session (not in our list yet)
// triggers a reload instead of being dropped.
func TestUI_EventForUnknownMangaReloads(t *testing.T) {
	ss := startSyncServer(t, "")
	h := startWith(t, func(h *harness) { h.tcpAddr = ss.addr })
	h.login("alice", "secret123")
	h.waitFor("connected", func() bool { return h.a.sync.state == tcpclient.Connected })
	h.key(tcell.KeyF3)
	h.waitFor("empty library", func() bool { return strings.Contains(h.a.status.GetText(true), "library is empty") })

	h.libMu.Lock()
	h.lib = map[string]*client.LibraryItem{"haikyu": {MangaID: "haikyu", Title: "Haikyu!!", Status: "reading",
		CurrentChapter: 7, TotalChapters: 402, Version: 2}}
	h.libMu.Unlock()
	ss.hub.SendToUser(1, tcpsync.ProgressEvent("haikyu", 7, "reading", 2), "x")
	h.waitFor("reloaded with new row", func() bool { return h.cell(1, 0) == "Haikyu!!" && h.cell(1, 2) == "7 / 402" })
}

// Logging out (here: an expired token) closes the TCP session on the server.
func TestUI_SessionExpiredClosesSync(t *testing.T) {
	ss := startSyncServer(t, "")
	h := startWith(t, func(h *harness) { h.tcpAddr = ss.addr })
	h.login("alice", "secret123")
	h.waitFor("connected", func() bool { return h.a.sync.state == tcpclient.Connected })
	h.ui(func() { h.a.sessionExpired(&client.APIError{Status: 401, Message: "expired"}) })
	h.waitFor("sync stopped", func() bool { return h.a.sync.state == tcpclient.Stopped && h.a.sync.cancel == nil })
	waitHub := func() bool { return ss.hub.Sessions(1) == 0 }
	h.waitFor("server dropped the session", waitHub)
}
