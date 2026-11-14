package api

import (
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// catalogFail turns a catalog error into the right HTTP status + message.
func catalogFail(c *gin.Context, err error) {
	code := httpStatus(err)
	msg := "catalog error"
	switch {
	case errors.Is(err, ErrMangaNotFound):
		msg = "manga not found"
	case errors.Is(err, ErrInvalidArgument):
		// gRPC InvalidArgument message is meant for humans: pass it on.
		msg = strings.TrimPrefix(err.Error(), ErrInvalidArgument.Error()+": ")
	case errors.Is(err, ErrCatalogUnavailable):
		msg = "catalog service unavailable, try again later"
	}
	log.Printf("catalog: %v → %d", err, code)
	fail(c, code, msg)
}

// GET /api/manga?q=...&limit=... → 200 {"items":[Manga...]} | 503
func (s *server) searchManga(c *gin.Context) {
	limit, _ := strconv.Atoi(c.Query("limit"))
	if limit < 0 {
		limit = 0
	}
	items, err := s.Catalog.Search(c.Request.Context(), strings.TrimSpace(c.Query("q")), limit)
	if err != nil {
		catalogFail(c, err)
		return
	}
	if items == nil {
		items = []Manga{}
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// GET /api/manga/:id → 200 Manga | 404 | 503
func (s *server) getManga(c *gin.Context) {
	id := c.Param("id")
	m, err := s.Catalog.Get(c.Request.Context(), id)
	if err != nil {
		catalogFail(c, err)
		return
	}
	c.JSON(http.StatusOK, m)
}
