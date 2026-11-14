// Package api is the Gin HTTP layer of the MangaHub backend.
package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"mangahub/internal/auth"
	"mangahub/internal/store"
)

// Deps are the collaborators wired together in cmd/backend.
type Deps struct {
	Store *store.Store
	Auth  *auth.Manager
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

	return r
}

// fail writes the contract's error body {"error": msg}.
func fail(c *gin.Context, code int, msg string) {
	c.AbortWithStatusJSON(code, gin.H{"error": msg})
}
