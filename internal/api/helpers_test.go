package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"mangahub/internal/auth"
	"mangahub/internal/store"
)

// ---------------------------------------------------------------- fakes

type fakeCatalog struct {
	mu    sync.Mutex
	down  bool
	manga map[string]Manga
}

func newFakeCatalog() *fakeCatalog {
	return &fakeCatalog{manga: map[string]Manga{
		"one-piece": {ID: "one-piece", Title: "One Piece", Author: "Eiichiro Oda", Genres: []string{"action"}, Status: "ongoing", TotalChapters: 1130},
		"naruto":    {ID: "naruto", Title: "Naruto", Author: "Masashi Kishimoto", Genres: []string{"action"}, Status: "completed", TotalChapters: 700},
	}}
}

func (f *fakeCatalog) setDown(v bool) { f.mu.Lock(); f.down = v; f.mu.Unlock() }

func (f *fakeCatalog) Search(_ context.Context, q string, _ int) ([]Manga, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return nil, ErrCatalogUnavailable
	}
	var out []Manga
	for _, m := range f.manga {
		if q == "" || bytes.Contains(bytes.ToLower([]byte(m.Title)), bytes.ToLower([]byte(q))) {
			out = append(out, m)
		}
	}
	return out, nil
}

func (f *fakeCatalog) Get(_ context.Context, id string) (Manga, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return Manga{}, ErrCatalogUnavailable
	}
	m, ok := f.manga[id]
	if !ok {
		return Manga{}, ErrMangaNotFound
	}
	return m, nil
}

func (f *fakeCatalog) UpdateChapters(_ context.Context, id string, total int) (Manga, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return Manga{}, ErrCatalogUnavailable
	}
	m, ok := f.manga[id]
	if !ok {
		return Manga{}, ErrMangaNotFound
	}
	if total < m.TotalChapters {
		return Manga{}, fmt.Errorf("%w: total_chapters cannot decrease", ErrInvalidArgument)
	}
	m.TotalChapters = total
	f.manga[id] = m
	return m, nil
}

// ---------------------------------------------------------------- harness

type env struct {
	t      *testing.T
	router *gin.Engine
	cat    *fakeCatalog
	dbPath string
	store  *store.Store
	jwt    *auth.Manager
}

func newEnv(t *testing.T) *env {
	t.Helper()
	gin.SetMode(gin.TestMode)
	e := &env{t: t, cat: newFakeCatalog(),
		dbPath: filepath.Join(t.TempDir(), "backend.db"), jwt: auth.NewManager("test-secret", time.Hour)}
	e.open()
	t.Cleanup(func() { e.store.Close() })
	return e
}

func (e *env) open() {
	st, err := store.Open(e.dbPath)
	if err != nil {
		e.t.Fatal(err)
	}
	e.store = st
	e.router = NewRouter(Deps{Store: st, Auth: e.jwt, Catalog: e.cat})
}

// restart simulates stopping and starting the backend on the same DB file.
func (e *env) restart() {
	e.store.Close()
	e.open()
}

func (e *env) do(method, path string, body any, hdr map[string]string) (int, map[string]any, http.Header) {
	e.t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if s, ok := body.(string); ok {
			buf.WriteString(s)
		} else {
			json.NewEncoder(&buf).Encode(body)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	e.router.ServeHTTP(w, req)
	var out map[string]any
	json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out, w.Header()
}

func bearer(tok string) map[string]string { return map[string]string{"Authorization": "Bearer " + tok} }

func (e *env) registerLogin(name string) string {
	e.t.Helper()
	cred := map[string]string{"username": name, "password": "secret123"}
	if code, body, _ := e.do("POST", "/api/auth/register", cred, nil); code != 201 {
		e.t.Fatalf("register %s: %d %v", name, code, body)
	}
	code, body, _ := e.do("POST", "/api/auth/login", cred, nil)
	if code != 200 {
		e.t.Fatalf("login %s: %d %v", name, code, body)
	}
	return body["token"].(string)
}

func expect(t *testing.T, what string, got, want int, body map[string]any) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: status %d, want %d (body %v)", what, got, want, body)
	}
	if want >= 400 {
		if _, ok := body["error"]; !ok {
			t.Fatalf("%s: error body missing \"error\" field: %v", what, body)
		}
	}
}
