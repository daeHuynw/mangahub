// Command backend runs the MangaHub Gin backend.
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

	"mangahub/internal/api"
	"mangahub/internal/auth"
	"mangahub/internal/config"
	"mangahub/internal/store"
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

	router := api.NewRouter(api.Deps{Store: st, Auth: jwt})
	httpSrv := &http.Server{Addr: cfg.HTTPAddr, Handler: router, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		log.Printf("http listening on %s", cfg.HTTPAddr)
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
}
