package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/gin-gonic/gin"

	"mangahub/internal/chat"

	"mangahub/internal/tui/client"
)

// These tests drive the real tview application on a tcell simulation screen:
// keys are injected like a user typing, and state is read back through
// QueueUpdate (inside the event loop), so `go test -race` also checks that no
// widget is touched from a network goroutine.

var catalogFixture = []client.Manga{
	{ID: "one-piece", Title: "One Piece", Author: "Eiichiro Oda", Genres: []string{"action", "adventure"}, Status: "ongoing", TotalChapters: 1130, Description: "Pirates."},
	{ID: "haikyu", Title: "Haikyu!!", Author: "Haruichi Furudate", Genres: []string{"sports"}, Status: "completed", TotalChapters: 402, Description: "Volleyball."},
	{ID: "broken", Title: "Broken Entry", Author: "X", Genres: []string{"action"}, Status: "ongoing", TotalChapters: 1, Description: "?"},
}

// fakeAPI is a minimal stand-in for the Gin backend (contracts.md §3).
func fakeAPI(t *testing.T, h *harness) *httptest.Server {
	t.Helper()
	write := func(w http.ResponseWriter, code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(v)
	}
	// /ws is served by the real chat hub (B7); token "tok:<name>" is <name>.
	gin.SetMode(gin.TestMode)
	h.chat = chat.New(func(tok string) (int64, string, error) {
		if name, ok := strings.CutPrefix(tok, "tok:"); ok && name != "" {
			return 1, name, nil
		}
		return 0, "", errors.New("bad token")
	})
	t.Cleanup(h.chat.Close)
	wsRouter := gin.New()
	wsRouter.GET("/ws", h.chat.Handler())

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ws" {
			wsRouter.ServeHTTP(w, r)
			return
		}
		h.requests.Add(1)
		var raw map[string]any
		json.NewDecoder(r.Body).Decode(&raw)
		body := map[string]string{}
		for k, v := range raw {
			if str, ok := v.(string); ok {
				body[k] = str
			}
		}
		authed := strings.HasPrefix(r.Header.Get("Authorization"), "Bearer tok:")
		switch {
		case r.URL.Path == "/api/health":
			write(w, 200, map[string]string{"status": "ok"})
		case r.URL.Path == "/api/auth/register":
			if body["username"] == "taken" {
				write(w, 409, map[string]string{"error": "username already taken"})
				return
			}
			write(w, 201, map[string]any{"id": 2, "username": body["username"]})
		case r.URL.Path == "/api/auth/login":
			if body["password"] != "secret123" {
				write(w, 401, map[string]string{"error": "invalid username or password"})
				return
			}
			tok := "tok:" + body["username"]
			if body["username"] == "expire" { // simulates a token the backend no longer accepts
				tok = "expired"
			}
			write(w, 200, map[string]any{"token": tok, "user": map[string]any{"id": 1, "username": body["username"]}})
		case !authed:
			write(w, 401, map[string]string{"error": "missing token"})
		case r.URL.Path == "/api/library" && r.Method == http.MethodGet:
			h.libMu.Lock()
			items := []client.LibraryItem{}
			for _, it := range h.lib {
				items = append(items, *it)
			}
			h.libMu.Unlock()
			write(w, 200, map[string]any{"items": items})
		case r.URL.Path == "/api/library" && r.Method == http.MethodPost:
			var m *client.Manga
			for i := range catalogFixture {
				if catalogFixture[i].ID == body["manga_id"] {
					m = &catalogFixture[i]
				}
			}
			if m == nil {
				write(w, 404, map[string]string{"error": "manga not found"})
				return
			}
			h.libMu.Lock()
			defer h.libMu.Unlock()
			if h.lib == nil {
				h.lib = map[string]*client.LibraryItem{}
			}
			if h.lib[m.ID] != nil {
				write(w, 409, map[string]string{"error": "manga already in your library"})
				return
			}
			it := &client.LibraryItem{MangaID: m.ID, Title: m.Title, Status: "plan-to-read", TotalChapters: m.TotalChapters,
				UpdatedAt: "2026-11-18T10:00:00Z", Version: 1}
			h.lib[m.ID] = it
			write(w, 201, it)
		case strings.HasPrefix(r.URL.Path, "/api/library/") && strings.HasSuffix(r.URL.Path, "/progress"):
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/library/"), "/progress")
			h.libMu.Lock()
			defer h.libMu.Unlock()
			it := h.lib[id]
			h.lastSessionID = r.Header.Get("X-Session-ID")
			if it == nil {
				write(w, 404, map[string]string{"error": "manga is not in your library"})
				return
			}
			ch, _ := raw["current_chapter"].(float64)
			h.putChapters = append(h.putChapters, int(ch))
			if ch == 13 { // a rejection the TUI's own check cannot predict
				write(w, 400, map[string]string{"error": "current_chapter must be between 0 and 12"})
				return
			}
			it.CurrentChapter, it.Version = int(ch), it.Version+1
			if body["status"] != "" {
				it.Status = body["status"]
			}
			write(w, 200, client.Progress{MangaID: id, CurrentChapter: it.CurrentChapter, Status: it.Status, Version: it.Version})
		case r.URL.Path == "/api/manga":
			q := strings.ToLower(r.URL.Query().Get("q"))
			if q == "" && h.failEmptySearch.Load() {
				write(w, 503, map[string]string{"error": "catalog service unavailable, try again later"})
				return
			}
			items := []client.Manga{}
			for _, m := range catalogFixture {
				if strings.Contains(strings.ToLower(m.Title), q) {
					items = append(items, m)
				}
			}
			write(w, 200, map[string]any{"items": items})
		case r.URL.Path == "/api/manga/broken":
			write(w, 503, map[string]string{"error": "catalog service unavailable, try again later"})
		case strings.HasPrefix(r.URL.Path, "/api/manga/"):
			id := strings.TrimPrefix(r.URL.Path, "/api/manga/")
			for _, m := range catalogFixture {
				if m.ID == id {
					w.Header().Set("X-Cache", "MISS")
					write(w, 200, m)
					return
				}
			}
			write(w, 404, map[string]string{"error": "manga not found"})
		default:
			write(w, 404, map[string]string{"error": "route not found"})
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

type harness struct {
	t        *testing.T
	a        *App
	screen   tcell.SimulationScreen
	requests atomic.Int32
	// failEmptySearch makes the fake answer 503 to the full-catalog query.
	failEmptySearch atomic.Bool

	libMu         sync.Mutex
	lib           map[string]*client.LibraryItem // the fake backend's library
	lastSessionID string                         // X-Session-ID of the last PUT
	putChapters   []int                          // current_chapter of every PUT, in order

	url     string       // fake backend base URL
	chat    *chat.Server // real chat hub behind /ws
	tcpAddr string       // TCP sync address given to the app ("" = unreachable)
	udpAddr string       // UDP alerts address ("" = unreachable)
}

// start runs the app against the fake backend.
func start(t *testing.T) *harness {
	t.Helper()
	return startWith(t, func(*harness) {})
}

// startWith lets a test configure the fake before the app starts.
func startWith(t *testing.T, setup func(*harness)) *harness {
	t.Helper()
	h := &harness{t: t}
	setup(h)
	srv := fakeAPI(t, h)
	h.url = srv.URL
	return h.run(srv.URL)
}

// another starts a second TUI against the same fake backend (and the same
// TCP/UDP servers), like opening a new terminal window.
func (h *harness) another() *harness {
	h2 := &harness{t: h.t, tcpAddr: h.tcpAddr, udpAddr: h.udpAddr, url: h.url}
	return h2.run(h.url)
}

// run starts the real App on a simulation screen against backend httpURL.
func (h *harness) run(httpURL string) *harness {
	t := h.t
	t.Helper()
	h.screen = tcell.NewSimulationScreen("UTF-8")
	tcp := h.tcpAddr
	if tcp == "" {
		tcp = "127.0.0.1:1" // nothing listens: the sync client just keeps retrying in the background
	}
	udp := h.udpAddr
	if udp == "" {
		udp = "127.0.0.1:1" // no notifier: alerts end up in "no ack", which is fine here
	}
	h.a = New(Config{HTTP: httpURL, TCP: tcp, UDP: udp}, WithScreen(h.screen))
	h.screen.SetSize(120, 40)
	done := make(chan error, 1)
	go func() { done <- h.a.Run() }()
	t.Cleanup(func() {
		h.a.Stop()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("app did not stop")
		}
	})
	return h
}

// ui runs f inside the event loop and waits for it. f must not call
// t.Fatal (that would kill the tview goroutine); t.Error is fine. If the
// event loop stopped, the test fails after 5 s instead of hanging.
func (h *harness) ui(f func()) {
	h.t.Helper()
	done := make(chan struct{})
	go func() {
		h.a.tv.QueueUpdate(f)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		h.t.Fatal("tview event loop is not responding (stopped or blocked)")
	}
}

func (h *harness) typeText(s string) {
	for _, r := range s {
		h.screen.InjectKey(tcell.KeyRune, r, tcell.ModNone)
	}
}

func (h *harness) key(k tcell.Key) { h.screen.InjectKey(k, 0, tcell.ModNone) }

// screenText returns what the simulation screen currently shows, as text.
func (h *harness) screenText() string {
	cells, w, ht := h.screen.GetContents()
	var b strings.Builder
	for y := 0; y < ht; y++ {
		line := make([]rune, 0, w)
		for x := 0; x < w; x++ {
			r := ' '
			if c := cells[y*w+x]; len(c.Runes) > 0 {
				r = c.Runes[0]
			}
			line = append(line, r)
		}
		b.WriteString(strings.TrimRight(string(line), " "))
		b.WriteByte('\n')
	}
	return b.String()
}

// waitFor polls cond (evaluated in the event loop) until it is true.
func (h *harness) waitFor(what string, cond func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ok := false
		h.ui(func() { ok = cond() })
		if ok {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	var page, status, details, msg string
	h.ui(func() {
		page, _ = h.a.pages.GetFrontPage()
		status = h.a.status.GetText(true)
		details = h.a.browser.details.GetText(true)
		msg = h.a.login.message.GetText(true)
	})
	h.t.Fatalf("timeout waiting for %s (page=%s status=%q login=%q details=%q)", what, page, status, msg, details)
}

func (h *harness) page() string {
	var p string
	h.ui(func() { p, _ = h.a.pages.GetFrontPage() })
	return p
}

// login fills the form like a user: username Enter, password Enter, Enter on "Login".
func (h *harness) login(user, pass string) {
	h.typeText(user)
	h.key(tcell.KeyEnter)
	h.typeText(pass)
	h.key(tcell.KeyEnter)
	h.key(tcell.KeyEnter)
}

func TestUI_WrongPasswordStaysOnLogin(t *testing.T) {
	h := start(t)
	h.login("alice", "wrongpass")
	h.waitFor("401 message", func() bool {
		return strings.Contains(h.a.login.message.GetText(true), "Wrong username or password")
	})
	if p := h.page(); p != pageLogin {
		t.Fatalf("page = %s, want login", p)
	}
}

func TestUI_LocalValidationSendsNoRequest(t *testing.T) {
	h := start(t)
	h.login("al", "x")
	h.waitFor("validation message", func() bool {
		return strings.Contains(h.a.login.message.GetText(true), "3-32 letters")
	})
	if n := h.requests.Load(); n != 0 {
		t.Fatalf("%d request(s) sent for invalid input", n)
	}
}

func TestUI_RegisterTakenShows409(t *testing.T) {
	h := start(t)
	h.typeText("taken")
	h.key(tcell.KeyEnter)
	h.typeText("secret123")
	h.key(tcell.KeyEnter)
	h.key(tcell.KeyTab) // Login button → Register button
	h.key(tcell.KeyEnter)
	h.waitFor("409 message", func() bool {
		return strings.Contains(h.a.login.message.GetText(true), "already taken")
	})
}

func TestUI_FKeysIgnoredBeforeLogin(t *testing.T) {
	h := start(t)
	h.key(tcell.KeyF6)
	h.key(tcell.KeyF2)
	time.Sleep(100 * time.Millisecond)
	if p := h.page(); p != pageLogin {
		t.Fatalf("page = %s; F-keys must not bypass login", p)
	}
}

func TestUI_LoginBrowseFilterSearchAndErrors(t *testing.T) {
	h := start(t)
	h.login("alice", "secret123")

	// 1. Login → browser with the whole catalog, first row's details loaded.
	h.waitFor("catalog listed", func() bool {
		return h.a.browser.list.GetItemCount() == 3 &&
			strings.Contains(h.a.browser.details.GetText(true), "Eiichiro Oda")
	})
	if p := h.page(); p != pageBrowser {
		t.Fatalf("page = %s", p)
	}
	h.ui(func() {
		if !strings.Contains(h.a.header.GetText(true), "alice") {
			t.Errorf("header does not show the user: %q", h.a.header.GetText(true))
		}
		if got := strings.Join(h.a.browser.genres, ","); got != allGenres+",action,adventure,sports" {
			t.Errorf("genre options = %s", got)
		}
	})
	details := ""
	h.ui(func() { details = h.a.browser.details.GetText(true) })
	for _, want := range []string{"One Piece", "action, adventure", "ongoing", "1130", "Pirates.", "MISS"} {
		if !strings.Contains(details, want) {
			t.Errorf("details missing %q:\n%s", want, details)
		}
	}

	// 2. Down arrow (list has focus) → details of the next manga.
	h.key(tcell.KeyDown)
	h.waitFor("second manga details", func() bool {
		return strings.Contains(h.a.browser.details.GetText(true), "Haruichi Furudate")
	})

	// 3. Third row returns 503 → friendly message, no crash.
	h.key(tcell.KeyDown)
	h.waitFor("503 message", func() bool {
		return strings.Contains(h.a.browser.details.GetText(true), "catalog service is unavailable")
	})

	// 4. Genre filter (the drop-down's callback) keeps only sports.
	h.ui(func() { h.a.browser.setGenre("sports") })
	h.waitFor("filtered list", func() bool {
		return h.a.browser.list.GetItemCount() == 1 &&
			strings.Contains(h.a.browser.details.GetText(true), "Haikyu!!")
	})
	h.ui(func() { h.a.browser.setGenre(allGenres) })

	// 5. "/" focuses search; typing + Enter searches through the backend.
	h.typeText("/")
	h.typeText("piece")
	h.key(tcell.KeyEnter)
	h.waitFor("search result", func() bool {
		return h.a.browser.list.GetItemCount() == 1 && h.a.browser.shown[0].ID == "one-piece"
	})

	// 6. No result → empty list and a clear message.
	h.ui(func() { h.a.browser.query.SetText("") })
	h.typeText("zzz")
	h.key(tcell.KeyEnter)
	h.waitFor("no-result message", func() bool {
		return h.a.browser.list.GetItemCount() == 0 &&
			strings.Contains(h.a.status.GetText(true), `No manga matches "zzz"`)
	})

	// 7. F6 → Connection Status with a live health check; Esc → back.
	h.key(tcell.KeyF6)
	h.waitFor("health check", func() bool {
		return strings.Contains(h.a.help.root.GetText(true), "reachable")
	})
	h.key(tcell.KeyEscape)
	h.waitFor("back to browser", func() bool { p, _ := h.a.pages.GetFrontPage(); return p == pageBrowser })
}

// Stale responses must not overwrite newer ones: the detail request sequence
// guard drops answers for a row that is no longer selected.
func TestUI_StaleDetailsAreDropped(t *testing.T) {
	h := start(t)
	h.login("alice", "secret123")
	h.waitFor("catalog listed", func() bool {
		return h.a.browser.list.GetItemCount() == 3 && strings.Contains(h.a.browser.details.GetText(true), "Eiichiro Oda")
	})
	var startSeq int
	h.ui(func() { startSeq = h.a.browser.detailSeq })
	// Move quickly down then back up: four detail requests, the last for row 0.
	h.key(tcell.KeyDown)
	h.key(tcell.KeyDown)
	h.key(tcell.KeyUp)
	h.key(tcell.KeyUp)
	// Wait until all four key presses were handled AND the last answer is in
	// (checking the text alone would pass at once with the old row-0 text).
	h.waitFor("row 0 details from the last request", func() bool {
		return h.a.browser.detailSeq == startSeq+4 && h.a.browser.list.GetCurrentItem() == 0 &&
			strings.Contains(h.a.browser.details.GetText(true), "Eiichiro Oda")
	})
	time.Sleep(200 * time.Millisecond) // let any late responses arrive
	// Read in the event loop, assert outside it: t.Fatal inside h.ui would
	// kill the tview goroutine and hang the test.
	var txt string
	h.ui(func() { txt = h.a.browser.details.GetText(true) })
	if !strings.Contains(txt, "Eiichiro Oda") {
		t.Fatalf("stale response replaced the details:\n%s", txt)
	}
}

// A 401 in the middle of a session (expired/revoked token) must clear the
// token and bring the login form back, not leave a broken browser screen.
func TestUI_ExpiredTokenGoesBackToLogin(t *testing.T) {
	h := start(t)
	h.login("expire", "secret123")
	h.waitFor("back on login", func() bool {
		p, _ := h.a.pages.GetFrontPage()
		return p == pageLogin && !h.a.loggedIn &&
			strings.Contains(h.a.login.message.GetText(true), "session expired")
	})
	if tok := h.a.api.Token(); tok != "" {
		t.Fatalf("token %q kept after 401", tok)
	}
	h.key(tcell.KeyF2) // F-keys are locked again
	time.Sleep(50 * time.Millisecond)
	if p := h.page(); p != pageLogin {
		t.Fatalf("page = %s after F2", p)
	}
}

// If the catalog is down right after login, the genre drop-down is filled by
// the first search that works.
func TestUI_GenresFilledWhenFirstLoadFails(t *testing.T) {
	h := startWith(t, func(h *harness) { h.failEmptySearch.Store(true) })
	h.login("alice", "secret123")
	h.waitFor("503 on first load", func() bool {
		return strings.Contains(h.a.status.GetText(true), "catalog service is unavailable")
	})
	h.typeText("/")
	h.typeText("piece")
	h.key(tcell.KeyEnter)
	h.waitFor("genres from search", func() bool {
		return strings.Join(h.a.browser.genres, ",") == allGenres+",action,adventure"
	})
}

// cell returns the text of a library table cell (event loop only).
func (h *harness) cell(row, col int) string {
	c := h.a.library.table.GetCell(row, col)
	if c == nil {
		return ""
	}
	return c.Text
}

func (h *harness) saveDialog() {
	h.key(tcell.KeyTab) // chapter -> status
	h.key(tcell.KeyTab) // status -> Save
	h.key(tcell.KeyEnter)
}

func (h *harness) setChapter(text string) {
	for i := 0; i < 6; i++ {
		h.key(tcell.KeyBackspace2)
	}
	h.typeText(text)
}

func TestUI_LibraryAddEditAndValidate(t *testing.T) {
	h := start(t)
	h.login("alice", "secret123")
	h.waitFor("catalog", func() bool { return h.a.browser.list.GetItemCount() == 3 })

	// Add the selected manga with "a"; a second "a" gets 409.
	h.typeText("a")
	h.waitFor("added", func() bool { return strings.Contains(h.a.status.GetText(true), "Added One Piece") })
	h.typeText("a")
	h.waitFor("409", func() bool { return strings.Contains(h.a.status.GetText(true), "already in your library") })

	// F3: the row is there.
	h.key(tcell.KeyF3)
	h.waitFor("library row", func() bool {
		return h.a.library.table.GetRowCount() == 2 && h.cell(1, 0) == "One Piece" && h.cell(1, 2) == "0 / 1130" && h.cell(1, 3) == "v1"
	})

	// Enter opens the dialog; save chapter 25 → v2.
	h.key(tcell.KeyEnter)
	h.waitFor("dialog", func() bool { p, _ := h.a.pages.GetFrontPage(); return p == pageUpdate })
	h.setChapter("25")
	h.saveDialog()
	h.waitFor("saved 25", func() bool {
		p, _ := h.a.pages.GetFrontPage()
		return p == pageLibrary && h.cell(1, 2) == "25 / 1130" && h.cell(1, 3) == "v2"
	})

	// Out of range: rejected locally, no request sent, dialog stays.
	h.key(tcell.KeyEnter)
	h.waitFor("dialog again", func() bool { p, _ := h.a.pages.GetFrontPage(); return p == pageUpdate })
	before := h.requests.Load()
	h.setChapter("9999")
	h.saveDialog()
	h.waitFor("local check", func() bool { return strings.Contains(h.a.library.formMsg.GetText(true), "between 0 and 1130") })
	if n := h.requests.Load(); n != before {
		t.Fatalf("%d request(s) sent for an out-of-range chapter", n-before)
	}

	// Backend rejects (400): its message is shown in the dialog.
	h.setChapter("13")
	h.saveDialog()
	h.waitFor("backend 400", func() bool {
		return strings.Contains(h.a.library.formMsg.GetText(true), "current_chapter must be between 0 and 12")
	})

	// Esc closes without saving.
	h.key(tcell.KeyEscape)
	h.waitFor("closed", func() bool { p, _ := h.a.pages.GetFrontPage(); return p == pageLibrary && h.cell(1, 2) == "25 / 1130" })

	// "+" and "-" change one chapter.
	h.typeText("+")
	h.waitFor("plus", func() bool { return h.cell(1, 2) == "26 / 1130" && h.cell(1, 3) == "v3" })
	h.typeText("-")
	h.waitFor("minus", func() bool { return h.cell(1, 2) == "25 / 1130" && h.cell(1, 3) == "v4" })

	// Status change through the dialog: Down in the drop-down picks "reading".
	h.key(tcell.KeyEnter)
	h.waitFor("dialog for status", func() bool { p, _ := h.a.pages.GetFrontPage(); return p == pageUpdate })
	h.ui(func() { h.a.library.status.SetCurrentOption(1) })
	h.saveDialog()
	h.waitFor("status saved", func() bool { return strings.Contains(h.cell(1, 1), "reading") && h.cell(1, 3) == "v5" })

	// Leaving with F2 and coming back reloads from the backend.
	h.key(tcell.KeyF2)
	h.key(tcell.KeyF3)
	h.waitFor("reloaded", func() bool { return h.cell(1, 2) == "25 / 1130" && h.cell(1, 3) == "v5" })
}

func TestUI_EmptyLibrary(t *testing.T) {
	h := start(t)
	h.login("alice", "secret123")
	h.waitFor("catalog", func() bool { return h.a.browser.list.GetItemCount() == 3 })
	h.key(tcell.KeyF3)
	h.waitFor("empty message", func() bool { return strings.Contains(h.a.status.GetText(true), "library is empty") })
	h.typeText("+") // nothing selected: must not crash or send anything
	h.key(tcell.KeyEnter)
	time.Sleep(50 * time.Millisecond)
	if p := h.page(); p != pageLibrary {
		t.Fatalf("page = %s", p)
	}
}

// Two quick "+" presses must not send the same "+1" twice (both used to read
// the old chapter before the first answer arrived).
func TestUI_QuickPlusDoesNotSendDuplicates(t *testing.T) {
	h := start(t)
	h.login("alice", "secret123")
	h.waitFor("catalog", func() bool { return h.a.browser.list.GetItemCount() == 3 })
	h.typeText("a")
	h.waitFor("added", func() bool { return strings.Contains(h.a.status.GetText(true), "Added") })
	h.key(tcell.KeyF3)
	h.waitFor("row", func() bool { return h.cell(1, 2) == "0 / 1130" })

	h.typeText("+++++") // five presses in one burst
	h.waitFor("settled", func() bool { return len(h.a.library.inFlight) == 0 && h.cell(1, 2) != "0 / 1130" })
	time.Sleep(100 * time.Millisecond)

	h.libMu.Lock()
	puts := append([]int(nil), h.putChapters...)
	h.libMu.Unlock()
	seen := map[int]bool{}
	for _, ch := range puts {
		if seen[ch] {
			t.Fatalf("chapter %d sent twice: %v", ch, puts)
		}
		seen[ch] = true
	}
	var shown string
	h.ui(func() { shown = h.cell(1, 2) })
	if want := fmt.Sprintf("%d / 1130", len(puts)); shown != want {
		t.Fatalf("row shows %q after PUTs %v, want %q", shown, puts, want)
	}
	t.Logf("5 presses -> PUTs %v", puts)
}
