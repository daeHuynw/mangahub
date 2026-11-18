package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeBackend records the last request and answers with handler.
type fakeBackend struct {
	mu      sync.Mutex
	lastReq *http.Request
	body    map[string]string
	srv     *httptest.Server
}

func newFake(t *testing.T, handler http.HandlerFunc) *fakeBackend {
	t.Helper()
	f := &fakeBackend{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.lastReq, f.body = r.Clone(context.Background()), body
		f.mu.Unlock()
		handler(w, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeBackend) last() (*http.Request, map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastReq, f.body
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func TestLogin_StoresTokenAndSendsBearer(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth/login":
			writeJSON(w, 200, map[string]any{"token": "jwt-123", "user": map[string]any{"id": 7, "username": "alice"}})
		default:
			writeJSON(w, 200, map[string]any{"items": []any{}})
		}
	})
	c := New(f.srv.URL + "/") // trailing slash must not produce "//api"

	u, err := c.Login(context.Background(), "alice", "secret123")
	if err != nil || u.ID != 7 || u.Username != "alice" {
		t.Fatalf("Login = %+v, %v", u, err)
	}
	req, body := f.last()
	if req.Method != http.MethodPost || req.URL.Path != "/api/auth/login" ||
		body["username"] != "alice" || body["password"] != "secret123" ||
		req.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("login request = %s %s %v %v", req.Method, req.URL.Path, body, req.Header)
	}
	if c.Token() != "jwt-123" || c.User().Username != "alice" {
		t.Fatalf("token/user not stored: %q %+v", c.Token(), c.User())
	}

	if _, err := c.SearchManga(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if req, _ := f.last(); req.Header.Get("Authorization") != "Bearer jwt-123" {
		t.Fatalf("Authorization = %q", req.Header.Get("Authorization"))
	}

	c.Logout()
	c.SearchManga(context.Background(), "")
	if req, _ := f.last(); req.Header.Get("Authorization") != "" || c.Token() != "" {
		t.Fatal("token still sent after Logout")
	}
}

func TestErrors_AreTypedWithBackendMessage(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"409 json", 409, `{"error":"username already taken"}`, "username already taken"},
		{"401 json", 401, `{"error":"invalid username or password"}`, "invalid username or password"},
		{"503 json", 503, `{"error":"catalog service unavailable, try again later"}`, "catalog service unavailable, try again later"},
		{"502 html", 502, `<html>bad gateway</html>`, "Bad Gateway"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			})
			_, err := New(f.srv.URL).Register(context.Background(), "alice", "secret123")
			var ae *APIError
			if !errors.As(err, &ae) || ae.Status != tc.status || ae.Message != tc.want {
				t.Fatalf("err = %#v, want APIError{%d, %q}", err, tc.status, tc.want)
			}
			if StatusOf(err) != tc.status {
				t.Fatalf("StatusOf = %d", StatusOf(err))
			}
		})
	}
	if StatusOf(errors.New("x")) != 0 {
		t.Fatal("StatusOf(non-API error) should be 0")
	}
}

func TestLogin_FailureKeepsLoggedOut(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 401, map[string]string{"error": "invalid username or password"})
	})
	c := New(f.srv.URL)
	if _, err := c.Login(context.Background(), "alice", "wrong"); StatusOf(err) != 401 {
		t.Fatalf("err = %v, want 401", err)
	}
	if c.Token() != "" {
		t.Fatal("token stored after failed login")
	}
}

func TestSearchManga(t *testing.T) {
	var gotQuery, gotLimit string
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("q")
		gotLimit = r.URL.Query().Get("limit")
		switch gotQuery {
		case "null":
			w.Write([]byte(`{"items":null}`))
		case "zzz":
			writeJSON(w, 200, map[string]any{"items": []any{}})
		default:
			writeJSON(w, 200, map[string]any{"items": []Manga{{ID: "one-piece", Title: "One Piece", Genres: []string{"action"}, TotalChapters: 1130}}})
		}
	})
	c := New(f.srv.URL)
	ctx := context.Background()

	items, err := c.SearchManga(ctx, "  one & piece?  ")
	if err != nil || len(items) != 1 || items[0].TotalChapters != 1130 {
		t.Fatalf("search = %+v, %v", items, err)
	}
	if gotQuery != "one & piece?" {
		t.Fatalf("query sent as %q; special characters must be URL-encoded and trimmed", gotQuery)
	}
	if gotLimit != "200" {
		t.Fatalf("limit = %q, want 200 (backend default 50 would cut the list)", gotLimit)
	}
	for _, q := range []string{"zzz", "null"} {
		items, err := c.SearchManga(ctx, q)
		if err != nil || items == nil || len(items) != 0 {
			t.Fatalf("q=%s: %#v, %v; want empty non-nil", q, items, err)
		}
	}
}

func TestGetManga(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.EscapedPath() {
		case "/api/manga/one-piece":
			w.Header().Set("X-Cache", "MISS")
			writeJSON(w, 200, Manga{ID: "one-piece", Title: "One Piece", Author: "Eiichiro Oda", Status: "ongoing", TotalChapters: 1130})
		case "/api/manga/a%2Fb":
			writeJSON(w, 404, map[string]string{"error": "manga not found"})
		default:
			writeJSON(w, 404, map[string]string{"error": "route not found: " + r.URL.EscapedPath()})
		}
	})
	c := New(f.srv.URL)
	ctx := context.Background()

	m, cache, err := c.GetManga(ctx, "one-piece")
	if err != nil || m.Author != "Eiichiro Oda" || cache != "MISS" {
		t.Fatalf("GetManga = %+v, %q, %v", m, cache, err)
	}
	// A "/" inside an id must be escaped, not change the route.
	if _, _, err := c.GetManga(ctx, "a/b"); StatusOf(err) != 404 || !strings.Contains(err.Error(), "manga not found") {
		t.Fatalf("escaped id: %v", err)
	}
	before, _ := f.last()
	if _, _, err := c.GetManga(ctx, "  "); StatusOf(err) != 400 {
		t.Fatalf("empty id: %v, want 400", err)
	}
	if after, _ := f.last(); after != before {
		t.Fatal("empty id should be rejected locally without a request")
	}
}

func TestSessionIDHeader(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	c := New(f.srv.URL)
	c.Health(context.Background())
	if req, _ := f.last(); req.Header.Get("X-Session-ID") != "" {
		t.Fatal("X-Session-ID sent before a TCP session exists")
	}
	c.SetSessionID("s7")
	c.Health(context.Background())
	if req, _ := f.last(); req.Header.Get("X-Session-ID") != "s7" {
		t.Fatalf("X-Session-ID = %q, want s7", req.Header.Get("X-Session-ID"))
	}
}

func TestUnreachableAndTimeout(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {})
	addr := f.srv.URL
	f.srv.Close() // nothing listens there now

	err := New(addr).Health(context.Background())
	if !errors.Is(err, ErrUnreachable) || StatusOf(err) != 0 {
		t.Fatalf("closed backend: %v, want ErrUnreachable", err)
	}

	slow := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := New(slow.srv.URL).Health(ctx); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("frozen backend: %v, want ErrUnreachable", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("context deadline not respected: %s", d)
	}
}

func TestBadJSONIsAnError(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"items": [`)) })
	if _, err := New(f.srv.URL).SearchManga(context.Background(), ""); err == nil {
		t.Fatal("want decode error")
	}
}

// Token/session updates from other goroutines while requests run (run with -race).
func TestConcurrentUse(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]any{"items": []any{}}) })
	c := New(f.srv.URL)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); c.SearchManga(context.Background(), "x") }()
		go func(i int) { defer wg.Done(); c.SetSessionID("s"); _ = c.Token(); _ = i }(i)
	}
	wg.Wait()
}
