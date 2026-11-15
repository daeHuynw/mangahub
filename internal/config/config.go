// Package config loads backend settings from environment variables.
// Every value has a default so `go run ./cmd/backend` works with no setup.
package config

import (
	"os"
	"time"
)

type Config struct {
	HTTPAddr    string // Gin HTTP + WebSocket listener
	TCPAddr     string // TCP progress-sync listener
	UDPAddr     string // UDP notification listener
	CatalogAddr string // address of the gRPC Catalog Service (client side)
	BackendDB   string // SQLite file for users + user_library
	JWTSecret   string // HS256 signing secret
	AdminKey    string // value expected in the X-Admin-Key header
	RedisAddr   string // empty = cache disabled

	CatalogTimeout time.Duration // deadline for every gRPC call
	CacheTTL       time.Duration // Redis TTL for GET /api/manga/:id
}

// Load reads the environment. Unset (or empty) variables fall back to defaults,
// except REDIS_ADDR where an explicitly empty value disables the cache.
func Load() Config {
	return Config{
		HTTPAddr:       env("HTTP_ADDR", ":8080"),
		TCPAddr:        env("TCP_ADDR", ":9090"),
		UDPAddr:        env("UDP_ADDR", ":9091"),
		CatalogAddr:    env("CATALOG_ADDR", "localhost:9092"),
		BackendDB:      env("BACKEND_DB", "backend.db"),
		JWTSecret:      env("JWT_SECRET", "dev-secret-change-me"),
		AdminKey:       env("ADMIN_KEY", "dev-admin-key"),
		RedisAddr:      envAllowEmpty("REDIS_ADDR", ""),
		CatalogTimeout: envDuration("CATALOG_TIMEOUT", 2*time.Second),
		CacheTTL:       envDuration("CACHE_TTL", 30*time.Second),
	}
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envAllowEmpty(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return def
}
