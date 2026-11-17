// Command catalog runs the MangaHub Catalog Service: a gRPC server (default
// :9092) backed by its own SQLite database (catalog.db). It is a separate
// process from the Gin backend, which reaches it only through gRPC.
//
// Environment (defaults match notes/contracts.md):
//
//	CATALOG_LISTEN  listen address           (default ":9092")
//	CATALOG_DB      SQLite file              (default "catalog.db")
//	CATALOG_SEED    seed JSON path override  (default: seed embedded in the binary)
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"mangahub/internal/catalog"
)

// shutdownTimeout bounds how long in-flight RPCs may take to finish after
// SIGINT/SIGTERM before the server closes them forcibly.
const shutdownTimeout = 5 * time.Second

type config struct {
	Listen string
	DB     string
	Seed   string // empty = embedded seed
}

func loadConfig() config {
	return config{
		Listen: env("CATALOG_LISTEN", ":9092"),
		DB:     env("CATALOG_DB", "catalog.db"),
		Seed:   os.Getenv(catalog.SeedEnv),
	}
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	// ctx is cancelled on the first Ctrl+C / SIGTERM. stop() restores the
	// default handlers, so a second Ctrl+C kills the process immediately.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, loadConfig(), nil); err != nil {
		log.Fatalf("catalog: %v", err)
	}
	log.Println("catalog: stopped")
}

// run opens the database, seeds it if empty, and serves gRPC until ctx is
// cancelled, then shuts down gracefully. onReady (optional, used by tests)
// receives the actual listen address once the port is bound.
func run(ctx context.Context, cfg config, onReady func(net.Addr)) error {
	repo, err := catalog.Open(cfg.DB)
	if err != nil {
		return err
	}
	// Deferred, so it runs only after the gRPC server below has fully stopped:
	// no RPC can use a closed database.
	defer repo.Close()

	seed, err := catalog.LoadSeed(cfg.Seed)
	if err != nil {
		return err
	}
	inserted, err := repo.SeedIfEmpty(ctx, seed)
	if err != nil {
		return fmt.Errorf("seed: %w", err)
	}
	total, err := repo.Count(ctx)
	if err != nil {
		return err
	}
	source := "embedded seed"
	if cfg.Seed != "" {
		source = cfg.Seed
	}
	if inserted > 0 {
		log.Printf("catalog: seeded %d manga from %s into %s", inserted, source, cfg.DB)
	} else {
		log.Printf("catalog: %s already has %d manga, seed skipped", cfg.DB, total)
	}

	lis, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen %s: %w", cfg.Listen, err)
	}
	srv := catalog.NewGRPCServer(repo)

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(lis) }()
	log.Printf("catalog: gRPC listening on %s (%d manga, db %s)", lis.Addr(), total, cfg.DB)
	if onReady != nil {
		onReady(lis.Addr())
	}

	select {
	case err := <-serveErr: // listener died on its own
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}

	// Graceful shutdown: stop accepting new connections/RPCs, let in-flight
	// RPCs finish, then return. If they take too long, force-close them.
	log.Printf("catalog: shutting down (waiting up to %s for in-flight RPCs)", shutdownTimeout)
	done := make(chan struct{})
	go func() {
		srv.GracefulStop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(shutdownTimeout):
		log.Printf("catalog: in-flight RPCs did not finish in %s, forcing stop", shutdownTimeout)
		srv.Stop()
		<-done
	}
	if err := <-serveErr; err != nil && !errors.Is(err, net.ErrClosed) {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}
