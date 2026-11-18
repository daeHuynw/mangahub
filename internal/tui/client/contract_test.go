package client

import (
	"context"
	"net"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"mangahub/data"
	"mangahub/internal/api"
	"mangahub/internal/auth"
	"mangahub/internal/catalog"
	"mangahub/internal/store"
)

// Contract test: this TUI client against member A's REAL Gin router, which
// calls B's REAL catalog over a real gRPC connection — the whole HTTP + gRPC
// path in one process (A's packages are only imported, never modified).
func TestContract_ClientAgainstRealBackendAndCatalog(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()

	repo, err := catalog.Open(filepath.Join(dir, "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	if _, err := repo.SeedIfEmpty(context.Background(), data.MangaSeed); err != nil {
		t.Fatal(err)
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	grpcSrv := catalog.NewGRPCServer(repo)
	go grpcSrv.Serve(lis)
	defer grpcSrv.Stop()

	st, err := store.Open(filepath.Join(dir, "backend.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cat, err := api.DialCatalog(lis.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer cat.Close()
	backend := httptest.NewServer(api.NewRouter(api.Deps{
		Store: st, Auth: auth.NewManager("test-secret", time.Hour), Catalog: cat, AdminKey: "k",
	}))
	defer backend.Close()

	c := New(backend.URL)
	ctx := context.Background()

	if err := c.Health(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Register(ctx, "alice", "secret123"); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, err := c.Register(ctx, "alice", "secret123"); StatusOf(err) != 409 {
		t.Fatalf("duplicate register: %v, want 409", err)
	}
	if _, err := c.Register(ctx, "x", "1"); StatusOf(err) != 400 {
		t.Fatalf("invalid register: %v, want 400", err)
	}
	if _, err := c.SearchManga(ctx, "piece"); StatusOf(err) != 401 {
		t.Fatalf("search before login: %v, want 401", err)
	}
	if _, err := c.Login(ctx, "alice", "wrong-pw"); StatusOf(err) != 401 {
		t.Fatalf("wrong password: %v, want 401", err)
	}
	if u, err := c.Login(ctx, "alice", "secret123"); err != nil || u.Username != "alice" {
		t.Fatalf("login: %+v, %v", u, err)
	}

	items, err := c.SearchManga(ctx, "piece")
	if err != nil || len(items) != 1 || items[0].ID != "one-piece" {
		t.Fatalf("search: %+v, %v", items, err)
	}
	all, err := c.SearchManga(ctx, "")
	if err != nil || len(all) < 30 {
		t.Fatalf("list all: %d, %v", len(all), err)
	}
	if none, err := c.SearchManga(ctx, "zzzzzz"); err != nil || len(none) != 0 {
		t.Fatalf("no match: %v, %v", none, err)
	}
	m, _, err := c.GetManga(ctx, "one-piece")
	if err != nil || m.Author != "Eiichiro Oda" || len(m.Genres) == 0 || m.Description == "" {
		t.Fatalf("detail: %+v, %v", m, err)
	}
	if _, _, err := c.GetManga(ctx, "does-not-exist"); StatusOf(err) != 404 {
		t.Fatalf("unknown: %v, want 404", err)
	}

	// Library (F03, HTTP part).
	if lib, err := c.ListLibrary(ctx); err != nil || len(lib) != 0 {
		t.Fatalf("empty library: %v, %v", lib, err)
	}
	added, err := c.AddToLibrary(ctx, "one-piece", "")
	if err != nil || added.Status != StatusPlanToRead || added.Title != "One Piece" || added.TotalChapters != 1130 || added.Version != 1 {
		t.Fatalf("add: %+v, %v", added, err)
	}
	if _, err := c.AddToLibrary(ctx, "one-piece", ""); StatusOf(err) != 409 {
		t.Fatalf("add duplicate: %v, want 409", err)
	}
	if _, err := c.AddToLibrary(ctx, "nope-nope", ""); StatusOf(err) != 404 {
		t.Fatalf("add unknown: %v, want 404", err)
	}
	p, err := c.UpdateProgress(ctx, "one-piece", 25, StatusReading)
	if err != nil || p.CurrentChapter != 25 || p.Status != StatusReading || p.Version != 2 {
		t.Fatalf("progress: %+v, %v", p, err)
	}
	if _, err := c.UpdateProgress(ctx, "one-piece", 99999, ""); StatusOf(err) != 400 {
		t.Fatalf("chapter > total: %v, want 400", err)
	}
	if _, err := c.UpdateProgress(ctx, "one-piece", -1, ""); StatusOf(err) != 400 {
		t.Fatalf("chapter < 0: %v, want 400", err)
	}
	if _, err := c.UpdateProgress(ctx, "naruto", 1, ""); StatusOf(err) != 404 {
		t.Fatalf("not in library: %v, want 404", err)
	}
	lib, err := c.ListLibrary(ctx)
	if err != nil || len(lib) != 1 || lib[0].CurrentChapter != 25 || lib[0].Version != 2 {
		t.Fatalf("library after update: %+v, %v", lib, err)
	}

	grpcSrv.Stop() // catalog down → the TUI gets a clean 503, not a hang
	if _, err := c.SearchManga(ctx, "piece"); StatusOf(err) != 503 {
		t.Fatalf("catalog stopped: %v, want 503", err)
	}
}
