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

// GET /api/manga/:id → 200 Manga (+X-Cache) | 404 | 503
func (s *server) getManga(c *gin.Context) {
	id := c.Param("id")
	var (
		m   Manga
		err error
	)
	if cr, ok := s.Catalog.(CacheReporter); ok {
		var st string
		m, st, err = cr.GetWithStatus(c.Request.Context(), id)
		if st != "" {
			c.Header("X-Cache", st)
		}
	} else {
		m, err = s.Catalog.Get(c.Request.Context(), id)
	}
	if err != nil {
		catalogFail(c, err)
		return
	}
	c.JSON(http.StatusOK, m)
}

// POST /api/admin/manga/:id/release {"total_chapters": n}
// → gRPC UpdateChapters → only if it succeeds, UDP broadcast.
// 200 {"manga_id","total_chapters","notified"} | 400 | 403 | 404 | 503
func (s *server) releaseChapter(c *gin.Context) {
	var body struct {
		TotalChapters *int `json:"total_chapters"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.TotalChapters == nil {
		fail(c, http.StatusBadRequest, "body must be {\"total_chapters\": <int>}")
		return
	}
	if *body.TotalChapters < 0 {
		fail(c, http.StatusBadRequest, "total_chapters must be >= 0")
		return
	}
	m, err := s.Catalog.UpdateChapters(c.Request.Context(), c.Param("id"), *body.TotalChapters)
	if err != nil {
		catalogFail(c, err) // no broadcast on failure
		return
	}
	notified := 0
	if s.Notify != nil {
		notified = s.Notify.Broadcast(m.ID, m.Title, m.TotalChapters)
	}
	c.JSON(http.StatusOK, gin.H{"manga_id": m.ID, "total_chapters": m.TotalChapters, "notified": notified})
}
