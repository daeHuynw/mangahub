package tui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"mangahub/internal/tui/tcpclient"
	"mangahub/internal/tui/udpclient"
)

// TestE2E_RealBackend drives the real TUI (on a simulation screen) against a
// RUNNING backend + catalog. It is skipped unless MANGAHUB_E2E_HTTP is set:
//
//	go run ./cmd/catalog   &   go run ./cmd/backend
//	MANGAHUB_E2E_HTTP=http://localhost:8080 go test -run E2E -v ./internal/tui/
//
// It registers a fresh user each run, so it can be repeated on the same DBs.
func TestE2E_RealBackend(t *testing.T) {
	base := os.Getenv("MANGAHUB_E2E_HTTP")
	if base == "" {
		t.Skip("set MANGAHUB_E2E_HTTP to run against a live backend")
	}
	h := (&harness{t: t, tcpAddr: e2eTCP(), udpAddr: e2eUDP()}).run(base)
	user := fmt.Sprintf("e2e%d", time.Now().UnixNano()%1_000_000_000)
	step := func(name string, start time.Time) {
		t.Logf("ok   %-48s %s", name, time.Since(start).Round(time.Millisecond))
	}

	// F01: register through the form (Tab to the Register button) → auto login.
	s := time.Now()
	h.typeText(user)
	h.key(tcell.KeyEnter)
	h.typeText("secret123")
	h.key(tcell.KeyEnter)
	h.key(tcell.KeyTab)
	h.key(tcell.KeyEnter)
	h.waitFor("register + login + catalog", func() bool {
		return h.a.loggedIn && h.a.browser.list.GetItemCount() >= 30
	})
	step(fmt.Sprintf("F01 register %s + login, F02 list catalog", user), s)

	// F02: details of the first row (all six fields, via Gin → gRPC).
	s = time.Now()
	h.waitFor("first details", func() bool {
		txt := h.a.browser.details.GetText(true)
		return strings.Contains(txt, "Author:") && strings.Contains(txt, "Chapters:") && !strings.Contains(txt, "Loading")
	})
	step("F02 details panel of first manga", s)

	// F02: search by title.
	s = time.Now()
	h.typeText("/")
	h.typeText("piece")
	h.key(tcell.KeyEnter)
	h.waitFor("search piece", func() bool {
		return h.a.browser.list.GetItemCount() == 1 && h.a.browser.shown[0].ID == "one-piece" &&
			strings.Contains(h.a.browser.details.GetText(true), "Eiichiro Oda")
	})
	step("F02 search 'piece' -> One Piece + details", s)
	if os.Getenv("MANGAHUB_E2E_DUMP") == "1" {
		h.ui(func() { h.a.tv.ForceDraw() })
		t.Logf("screen:\n%s", h.screenText())
	}

	// F02: search with no results.
	s = time.Now()
	h.ui(func() { h.a.browser.query.SetText("") })
	h.typeText("zzzzzz")
	h.key(tcell.KeyEnter)
	h.waitFor("no results", func() bool {
		return h.a.browser.list.GetItemCount() == 0 && strings.Contains(h.a.status.GetText(true), "No manga matches")
	})
	step("F02 search 'zzzzzz' -> empty list + message", s)

	// Genre filter on the full catalog.
	s = time.Now()
	h.ui(func() { h.a.browser.query.SetText("") })
	h.key(tcell.KeyEnter)
	h.waitFor("full list again", func() bool { return h.a.browser.list.GetItemCount() >= 30 })
	h.ui(func() { h.a.browser.setGenre("sports") })
	h.waitFor("sports filter", func() bool {
		n := h.a.browser.list.GetItemCount()
		ok := n >= 1 && n < 30
		for _, m := range h.a.browser.shown {
			ok = ok && strings.Contains(strings.Join(m.Genres, ","), "sports")
		}
		return ok
	})
	n := 0
	h.ui(func() { n = h.a.browser.list.GetItemCount() })
	step(fmt.Sprintf("genre filter 'sports' -> %d manga", n), s)

	// Connection status screen with a live health check.
	s = time.Now()
	h.key(tcell.KeyF6)
	h.waitFor("health", func() bool { return strings.Contains(h.a.help.root.GetText(true), "reachable") })
	h.key(tcell.KeyEscape)
	step("F6 connection status: backend reachable", s)

	// F03 (HTTP part): add from the browser, edit in My Library.
	s = time.Now()
	h.ui(func() { h.a.browser.setGenre(allGenres); h.a.browser.query.SetText("") })
	h.typeText("/")
	h.typeText("piece")
	h.key(tcell.KeyEnter)
	h.waitFor("one-piece row", func() bool { return h.a.browser.list.GetItemCount() == 1 })
	h.key(tcell.KeyTab) // search -> genre
	h.key(tcell.KeyTab) // genre -> list
	h.typeText("a")
	h.waitFor("added", func() bool { return strings.Contains(h.a.status.GetText(true), "Added One Piece") })
	h.typeText("a")
	h.waitFor("409", func() bool { return strings.Contains(h.a.status.GetText(true), "already in your library") })
	step("F03 add One Piece (201), add again (409)", s)

	s = time.Now()
	h.key(tcell.KeyF3)
	h.waitFor("library row", func() bool {
		return h.cell(1, 0) == "One Piece" && strings.HasPrefix(h.cell(1, 2), "0 / ") && h.cell(1, 3) == "v1"
	})
	h.typeText("+")
	h.waitFor("chapter 1 v2", func() bool { return strings.HasPrefix(h.cell(1, 2), "1 / ") && h.cell(1, 3) == "v2" })
	step("F03 My Library row, + -> chapter 1 (v2)", s)

	s = time.Now()
	h.key(tcell.KeyEnter)
	h.waitFor("dialog", func() bool { p, _ := h.a.pages.GetFrontPage(); return p == pageUpdate })
	h.setChapter("25")
	h.saveDialog()
	h.waitFor("chapter 25 v3", func() bool { return strings.HasPrefix(h.cell(1, 2), "25 / ") && h.cell(1, 3) == "v3" })
	h.key(tcell.KeyEnter)
	h.waitFor("dialog again", func() bool { p, _ := h.a.pages.GetFrontPage(); return p == pageUpdate })
	h.setChapter("99999")
	h.saveDialog()
	h.waitFor("range error", func() bool { return strings.Contains(h.a.library.formMsg.GetText(true), "between 0 and") })
	h.key(tcell.KeyEscape)
	step("F03 dialog: save 25 (v3), 99999 rejected", s)

	// Persistence: a brand-new TUI session (fresh login) sees the same row.
	s = time.Now()
	h2 := (&harness{t: t, tcpAddr: e2eTCP(), udpAddr: e2eUDP()}).run(base)
	h2.login(user, "secret123")
	h2.waitFor("second session logged in", func() bool { return h2.a.loggedIn })
	h2.key(tcell.KeyF3)
	h2.waitFor("same row in session 2", func() bool {
		return strings.HasPrefix(h2.cell(1, 2), "25 / ") && h2.cell(1, 3) == "v3"
	})
	step("second session sees chapter 25 v3 (stored in SQLite)", s)

	// F03 (TCP part): both sessions hold a sync connection; "+" in session 1
	// shows up in session 2 without any key press there.
	s = time.Now()
	h.waitFor("session 1 sync", func() bool { return h.a.sync.state == tcpclient.Connected })
	h2.waitFor("session 2 sync", func() bool { return h2.a.sync.state == tcpclient.Connected })
	var sid1, sid2 string
	h.ui(func() { sid1 = h.a.sync.sessionID })
	h2.ui(func() { sid2 = h2.a.sync.sessionID })
	step(fmt.Sprintf("TCP sessions: %s (A) and %s (B), same user", sid1, sid2), s)

	// User C on a third TUI: connected too, must not see A's private event.
	s = time.Now()
	other := fmt.Sprintf("c%d", time.Now().UnixNano()%1_000_000_000)
	h3 := (&harness{t: t, tcpAddr: e2eTCP(), udpAddr: e2eUDP()}).run(base)
	h3.typeText(other)
	h3.key(tcell.KeyEnter)
	h3.typeText("secret123")
	h3.key(tcell.KeyEnter)
	h3.key(tcell.KeyTab)
	h3.key(tcell.KeyEnter)
	h3.waitFor("user C sync", func() bool { return h3.a.loggedIn && h3.a.sync.state == tcpclient.Connected })
	h3.key(tcell.KeyF3)
	h3.waitFor("user C empty library", func() bool { return strings.Contains(h3.a.status.GetText(true), "library is empty") })
	step(fmt.Sprintf("user C %s connected on a third TUI", other), s)

	s = time.Now()
	h.ui(func() {}) // session 1 is on My Library with the One Piece row selected
	h.typeText("+")
	h.waitFor("A saved 26", func() bool { return strings.HasPrefix(h.cell(1, 2), "26 / ") && h.cell(1, 3) == "v4" })
	h2.waitFor("B updated without refresh", func() bool {
		return strings.HasPrefix(h2.cell(1, 2), "26 / ") && h2.cell(1, 3) == "v4" &&
			strings.Contains(h2.a.status.GetText(true), "Synced from another session")
	})
	step("A: + -> 26 v4; B shows 26 v4 via TCP push", s)

	time.Sleep(300 * time.Millisecond)
	var cStatus string
	var cRows int
	h3.ui(func() { cStatus = h3.a.status.GetText(true); cRows = len(h3.a.library.state.items) })
	if strings.Contains(cStatus, "Synced") || cRows != 0 {
		t.Fatalf("user C saw user A's event: status %q, %d rows", cStatus, cRows)
	}
	step("user C received nothing", time.Now())

	// F04: A and B are subscribed to UDP alerts; C logs out (unsubscribe).
	s = time.Now()
	h.waitFor("A subscribed", func() bool { return h.a.alerts.state == udpclient.Subscribed })
	h2.waitFor("B subscribed", func() bool { return h2.a.alerts.state == udpclient.Subscribed })
	h3.screen.InjectKey(tcell.KeyCtrlL, 0, tcell.ModCtrl)
	h3.waitFor("C logged out", func() bool { return !h3.a.loggedIn })
	time.Sleep(200 * time.Millisecond) // let the unsubscribe datagram arrive
	step("A, B subscribed; C logged out (unsubscribed)", s)

	s = time.Now()
	var total int
	h.ui(func() { it, _ := h.a.library.state.get("one-piece"); total = it.TotalChapters })
	notified := release(t, base, "one-piece", total+1)
	h.waitFor("A alert", func() bool {
		return strings.Contains(h.a.status.GetText(true), fmt.Sprintf("now has %d chapters", total+1))
	})
	h2.waitFor("B alert and library count", func() bool {
		return len(h2.a.notifications.items) == 1 && h2.cell(1, 2) == fmt.Sprintf("26 / %d", total+1)
	})
	step(fmt.Sprintf("release one-piece -> %d: notified=%d, A and B show the alert", total+1, notified), s)

	time.Sleep(300 * time.Millisecond)
	var cItems int
	h3.ui(func() { cItems = len(h3.a.notifications.items) })
	if cItems != 0 {
		t.Fatalf("logged-out user C got %d alert(s)", cItems)
	}
	step("C (unsubscribed) got no alert", time.Now())
}

// release calls the admin endpoint (POST /api/admin/manga/:id/release) and
// returns the backend's "notified" count.
func release(t *testing.T, base, id string, total int) int {
	t.Helper()
	key := os.Getenv("MANGAHUB_E2E_ADMIN_KEY")
	if key == "" {
		key = "dev-admin-key"
	}
	body := strings.NewReader(fmt.Sprintf(`{"total_chapters":%d}`, total))
	req, _ := http.NewRequest(http.MethodPost, base+"/api/admin/manga/"+id+"/release", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Admin-Key", key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Notified int    `json:"notified"`
		Error    string `json:"error"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("release: HTTP %d %s", resp.StatusCode, out.Error)
	}
	return out.Notified
}

// e2eUDP is the alerts address for live runs (MANGAHUB_E2E_UDP, default :9091).
func e2eUDP() string {
	if v := os.Getenv("MANGAHUB_E2E_UDP"); v != "" {
		return v
	}
	return "localhost:9091"
}

// e2eTCP is the sync address for live runs (MANGAHUB_E2E_TCP, default :9090).
func e2eTCP() string {
	if v := os.Getenv("MANGAHUB_E2E_TCP"); v != "" {
		return v
	}
	return "localhost:9090"
}

// TestE2E_CatalogDown: run with the backend up and the catalog STOPPED
// (MANGAHUB_E2E_CATALOG_DOWN=1). Login still works (users live in the
// backend's own SQLite) but searching shows the controlled 503 message.
func TestE2E_CatalogDown(t *testing.T) {
	base := os.Getenv("MANGAHUB_E2E_HTTP")
	if base == "" || os.Getenv("MANGAHUB_E2E_CATALOG_DOWN") != "1" {
		t.Skip("set MANGAHUB_E2E_HTTP and MANGAHUB_E2E_CATALOG_DOWN=1 with the catalog stopped")
	}
	h := (&harness{t: t}).run(base)
	user := fmt.Sprintf("down%d", time.Now().UnixNano()%1_000_000_000)
	s := time.Now()
	h.typeText(user)
	h.key(tcell.KeyEnter)
	h.typeText("secret123")
	h.key(tcell.KeyEnter)
	h.key(tcell.KeyTab)
	h.key(tcell.KeyEnter)
	h.waitFor("503 after login", func() bool {
		return h.a.loggedIn && strings.Contains(h.a.status.GetText(true), "catalog service is unavailable")
	})
	t.Logf("ok   login works, catalog search -> controlled 503 message (%s)", time.Since(s).Round(time.Millisecond))
	h.ui(func() {
		if p, _ := h.a.pages.GetFrontPage(); p != pageBrowser {
			t.Errorf("page = %s; a 503 must not log the user out", p)
		}
	})
}
