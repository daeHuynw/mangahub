package tui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.requests.Add(1)
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		authed := r.Header.Get("Authorization") == "Bearer tok"
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
			tok := "tok"
			if body["username"] == "expire" { // simulates a token the backend no longer accepts
				tok = "expired"
			}
			write(w, 200, map[string]any{"token": tok, "user": map[string]any{"id": 1, "username": body["username"]}})
		case !authed:
			write(w, 401, map[string]string{"error": "missing token"})
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
	return h.run(srv.URL)
}

// run starts the real App on a simulation screen against backend httpURL.
func (h *harness) run(httpURL string) *harness {
	t := h.t
	t.Helper()
	h.screen = tcell.NewSimulationScreen("UTF-8")
	h.a = New(Config{HTTP: httpURL, TCP: "localhost:9090", UDP: "localhost:9091"}, WithScreen(h.screen))
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

// ui runs f inside the event loop and waits for it.
func (h *harness) ui(f func()) { h.a.tv.QueueUpdate(f) }

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
	h.waitFor("catalog listed", func() bool { return h.a.browser.list.GetItemCount() == 3 })
	// Move quickly down then back up: the final text must be row 0.
	h.key(tcell.KeyDown)
	h.key(tcell.KeyDown)
	h.key(tcell.KeyUp)
	h.key(tcell.KeyUp)
	h.waitFor("row 0 details", func() bool {
		return strings.Contains(h.a.browser.details.GetText(true), "Eiichiro Oda")
	})
	time.Sleep(200 * time.Millisecond) // let any late responses arrive
	h.ui(func() {
		if txt := h.a.browser.details.GetText(true); !strings.Contains(txt, "Eiichiro Oda") {
			t.Fatalf("stale response replaced the details:\n%s", txt)
		}
	})
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
