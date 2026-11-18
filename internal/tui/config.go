package tui

import "os"

// Config holds the backend addresses. Defaults match notes/contracts.md §1.
type Config struct {
	HTTP string // MANGAHUB_HTTP, Gin REST + WebSocket
	TCP  string // MANGAHUB_TCP, progress sync (B5)
	UDP  string // MANGAHUB_UDP, chapter alerts (B6)
}

// LoadConfig reads the environment; unset or empty variables use defaults.
func LoadConfig() Config {
	return Config{
		HTTP: env("MANGAHUB_HTTP", "http://localhost:8080"),
		TCP:  env("MANGAHUB_TCP", "localhost:9090"),
		UDP:  env("MANGAHUB_UDP", "localhost:9091"),
	}
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
