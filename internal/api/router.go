// Package api is the Gin HTTP layer of the MangaHub backend.
package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"mangahub/internal/auth"
	"mangahub/internal/store"
	"mangahub/internal/tcpsync"
)

// ProgressPusher is implemented by *tcpsync.Hub.
type ProgressPusher interface {
	SendToUser(uid int64, ev tcpsync.Event, exceptSession string) int
}

// Broadcaster is implemented by *udpnotify.Notifier.
type Broadcaster interface {
	Broadcast(mangaID, title string, totalChapters int) int
}

// Deps are the collaborators wired together in cmd/backend.
type Deps struct {
	Store    *store.Store
	Auth     *auth.Manager
	Catalog  Catalog
	Sync     ProgressPusher // TCP (F03)
	Notify   Broadcaster    // UDP (F04)
	AdminKey string
	// WS is the WebSocket upgrade handler owned by member B (internal/chat).
	// When set it is mounted at GET /ws.
	WS gin.HandlerFunc
}

type server struct{ Deps }

// NewRouter builds the Gin engine with every MangaHub route.
func NewRouter(d Deps) *gin.Engine {
	s := &server{d}
	r := gin.New()
	r.Use(gin.Logger(), gin.Recovery())
	r.NoRoute(func(c *gin.Context) { c.JSON(http.StatusNotFound, gin.H{"error": "route not found"}) })

	api := r.Group("/api")
	api.GET("/health", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })
	api.POST("/auth/register", s.register)
	api.POST("/auth/login", s.login)

	authed := api.Group("", d.Auth.RequireAuth())
	authed.GET("/me", s.me)
	authed.GET("/manga", s.searchManga)
	authed.GET("/manga/:id", s.getManga)
	authed.GET("/library", s.listLibrary)
	authed.POST("/library", s.addToLibrary)
	authed.PUT("/library/:mangaId/progress", s.updateProgress)

	admin := api.Group("/admin", auth.RequireAdmin(d.AdminKey))
	admin.POST("/manga/:id/release", s.releaseChapter)

	if d.WS != nil {
		r.GET("/ws", d.WS)
	}
	return r
}

// fail writes the contract's error body {"error": msg}.
func fail(c *gin.Context, code int, msg string) {
	c.AbortWithStatusJSON(code, gin.H{"error": msg})
}
