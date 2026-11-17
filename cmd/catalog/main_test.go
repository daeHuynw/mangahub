package main

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"mangahub/gen/catalogpb"
)

// start runs the real catalog program logic on a dynamic port and returns a
// client plus a stop function that triggers the same path as Ctrl+C.
func start(t *testing.T, cfg config) (catalogpb.CatalogServiceClient, func() error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan net.Addr, 1)
	done := make(chan error, 1)
	go func() { done <- run(ctx, cfg, func(a net.Addr) { ready <- a }) }()

	var addr net.Addr
	select {
	case addr = <-ready:
	case err := <-done:
		cancel()
		t.Fatalf("run exited early: %v", err)
	case <-time.After(10 * time.Second):
		cancel()
		t.Fatal("catalog did not start")
	}
	conn, err := grpc.NewClient(addr.String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	stop := func() error {
		conn.Close()
		cancel()
		select {
		case err := <-done:
			return err
		case <-time.After(10 * time.Second):
			t.Fatal("run did not return after cancel")
			return nil
		}
	}
	return catalogpb.NewCatalogServiceClient(conn), stop
}

// Full lifecycle: start → seed → serve → shutdown → restart on the same file
// keeps the released chapter and does not re-seed.
func TestRun_SeedServeShutdownRestart(t *testing.T) {
	cfg := config{Listen: "127.0.0.1:0", DB: filepath.Join(t.TempDir(), "catalog.db")}
	ctx := context.Background()

	c, stop := start(t, cfg)
	if _, err := c.UpdateChapters(ctx, &catalogpb.UpdateChaptersRequest{Id: "naruto", TotalChapters: 701}); err != nil {
		t.Fatal(err)
	}
	if err := stop(); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	c, stop = start(t, cfg)
	defer stop()
	m, err := c.GetManga(ctx, &catalogpb.GetMangaRequest{Id: "naruto"})
	if err != nil || m.GetTotalChapters() != 701 {
		t.Fatalf("after restart: %v, %v; want 701 (persisted, not re-seeded)", m, err)
	}
	all, err := c.SearchManga(ctx, &catalogpb.SearchMangaRequest{})
	if err != nil || len(all.GetItems()) < 30 {
		t.Fatalf("after restart: %d manga, %v", len(all.GetItems()), err)
	}
}

func TestRun_MissingSeedFileFails(t *testing.T) {
	cfg := config{
		Listen: "127.0.0.1:0",
		DB:     filepath.Join(t.TempDir(), "catalog.db"),
		Seed:   filepath.Join(t.TempDir(), "missing.json"),
	}
	if err := run(context.Background(), cfg, nil); err == nil {
		t.Fatal("want error for missing CATALOG_SEED file")
	}
}

func TestRun_PortInUseFails(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	cfg := config{Listen: busy.Addr().String(), DB: filepath.Join(t.TempDir(), "catalog.db")}
	if err := run(context.Background(), cfg, nil); err == nil {
		t.Fatal("want error when the port is already in use")
	}
}

func TestLoadConfig_Defaults(t *testing.T) {
	t.Setenv("CATALOG_LISTEN", "")
	t.Setenv("CATALOG_DB", "")
	t.Setenv("CATALOG_SEED", "")
	cfg := loadConfig()
	if cfg.Listen != ":9092" || cfg.DB != "catalog.db" || cfg.Seed != "" {
		t.Fatalf("defaults = %+v, want :9092 / catalog.db / embedded", cfg)
	}
	t.Setenv("CATALOG_LISTEN", ":7000")
	t.Setenv("CATALOG_SEED", "x.json")
	if cfg := loadConfig(); cfg.Listen != ":7000" || cfg.Seed != "x.json" {
		t.Fatalf("env override = %+v", cfg)
	}
}
