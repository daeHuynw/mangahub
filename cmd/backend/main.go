// Command backend runs the MangaHub Gin backend: HTTP (+WebSocket route),
// TCP progress sync and UDP notifications in one process. Catalog data comes
// from the separate gRPC Catalog Service (cmd/catalog).
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"

	"mangahub/internal/api"
	"mangahub/internal/auth"
	"mangahub/internal/cache"
	"mangahub/internal/chat"
	"mangahub/internal/config"
	"mangahub/internal/store"
	"mangahub/internal/tcpsync"
	"mangahub/internal/udpnotify"
)

func main() {
	cfg := config.Load()
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	st, err := store.Open(cfg.BackendDB)
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	defer st.Close()

	jwt := auth.NewManager(cfg.JWTSecret, auth.DefaultTTL)
	verify := func(tok string) (int64, error) {
		c, err := jwt.Verify(tok)
		if err != nil {
			return 0, err
		}
		return c.UID, nil
	}

	grpcCatalog, err := api.DialCatalog(cfg.CatalogAddr, cfg.CatalogTimeout)
	if err != nil {
		log.Fatalf("catalog: %v", err)
	}
	defer grpcCatalog.Close()
	var catalog api.Catalog = grpcCatalog

	if cfg.RedisAddr != "" {
		rdb := redis.NewClient(&redis.Options{Addr: cfg.RedisAddr})
		defer rdb.Close()
		pctx, cancel := context.WithTimeout(context.Background(), time.Second)
		if err := rdb.Ping(pctx).Err(); err != nil {
			log.Printf("redis %s not reachable (%v): cache will fall back to gRPC until it is", cfg.RedisAddr, err)
		}
		cancel()
		catalog = cache.New(grpcCatalog, rdb, cfg.CacheTTL)
		log.Printf("redis cache enabled at %s, TTL %s", cfg.RedisAddr, cfg.CacheTTL)
	}

	hub := tcpsync.NewHub()
	tcpSrv := tcpsync.NewServer(hub, verify)
	go func() {
		if err := tcpSrv.ListenAndServe(cfg.TCPAddr); err != nil {
			log.Fatalf("tcp %s: %v", cfg.TCPAddr, err)
		}
	}()

	notifier := udpnotify.New(verify, udpnotify.DefaultTTL)
	go func() {
		if err := notifier.ListenAndServe(cfg.UDPAddr); err != nil {
			log.Fatalf("udp %s: %v", cfg.UDPAddr, err)
		}
	}()

	router := api.NewRouter(api.Deps{
		Store:    st,
		Auth:     jwt,
		Catalog:  catalog,
		Sync:     hub,
		Notify:   notifier,
		AdminKey: cfg.AdminKey,
		WS:       chat.New(chat.JWTVerifier(jwt)).Handler(), // GET /ws, internal/chat (member B)
	})
	httpSrv := &http.Server{Addr: cfg.HTTPAddr, Handler: router, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		log.Printf("http listening on %s (catalog %s, timeout %s)", cfg.HTTPAddr, cfg.CatalogAddr, cfg.CatalogTimeout)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("http %s: %v", cfg.HTTPAddr, err)
		}
	}()

	// Graceful shutdown on Ctrl+C / SIGTERM so SQLite is closed cleanly.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	log.Println("shutting down...")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	httpSrv.Shutdown(ctx)
	tcpSrv.Close()
	notifier.Close()
}
