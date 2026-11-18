package tui

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
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
	h := (&harness{t: t}).run(base)
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
