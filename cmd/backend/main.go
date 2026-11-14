// Command backend runs the MangaHub Gin backend.
package main

import (
	"log"
	"net/http"

	"github.com/gin-gonic/gin"

	"mangahub/internal/config"
)

func main() {
	cfg := config.Load()

	r := gin.New()
	r.Use(gin.Logger(), gin.Recovery())
	r.GET("/api/health", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })

	log.Printf("http listening on %s", cfg.HTTPAddr)
	if err := r.Run(cfg.HTTPAddr); err != nil {
		log.Fatalf("http %s: %v", cfg.HTTPAddr, err)
	}
}
