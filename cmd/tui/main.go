// Command tui is the MangaHub terminal client (tview). It talks only to the
// Gin backend: HTTP now, TCP/UDP/WebSocket in later stages.
//
// Environment (defaults match notes/contracts.md):
//
//	MANGAHUB_HTTP  backend REST URL  (default "http://localhost:8080")
//	MANGAHUB_TCP   progress sync     (default "localhost:9090")
//	MANGAHUB_UDP   chapter alerts    (default "localhost:9091")
//	MANGAHUB_LOG   log file          (default "tui.log"; the terminal belongs to the UI)
package main

import (
	"fmt"
	"io"
	"log"
	"os"

	"mangahub/internal/tui"
)

func main() {
	// Anything printed to the terminal would corrupt the tview screen, so
	// logs go to a file (tui.log is git-ignored).
	logPath := os.Getenv("MANGAHUB_LOG")
	if logPath == "" {
		logPath = "tui.log"
	}
	if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		defer f.Close()
		log.SetOutput(f)
	} else {
		log.SetOutput(io.Discard)
	}

	cfg := tui.LoadConfig()
	log.Printf("tui start: http=%s tcp=%s udp=%s", cfg.HTTP, cfg.TCP, cfg.UDP)
	if err := tui.New(cfg).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "tui:", err)
		os.Exit(1)
	}
}
