package api

import (
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"mangahub/internal/auth"
	"mangahub/internal/store"
)

// LibraryItem is the JSON shape of one library row (contracts.md §2).
type LibraryItem struct {
	MangaID        string `json:"manga_id"`
	Title          string `json:"title"`
	Status         string `json:"status"`
	CurrentChapter int    `json:"current_chapter"`
	TotalChapters  int    `json:"total_chapters"`
	UpdatedAt      string `json:"updated_at"`
	Version        int64  `json:"version"`
}

func toItem(e store.LibraryEntry, m Manga) LibraryItem {
	return LibraryItem{
		MangaID:        e.MangaID,
		Title:          m.Title,
		Status:         e.Status,
		CurrentChapter: e.CurrentChapter,
		TotalChapters:  m.TotalChapters,
		UpdatedAt:      e.UpdatedAt.UTC().Format(time.RFC3339),
		Version:        e.Version,
	}
}

func storeFail(c *gin.Context, op string, err error) {
	log.Printf("%s: %v", op, err)
	fail(c, http.StatusInternalServerError, "internal error")
}

// GET /api/library → 200 {"items":[LibraryItem...]} | 503
func (s *server) listLibrary(c *gin.Context) {
	uid := auth.UserID(c) // always from the verified JWT
	entries, err := s.Store.ListLibrary(c.Request.Context(), uid)
	if err != nil {
		storeFail(c, "list library", err)
		return
	}
	items := make([]LibraryItem, 0, len(entries))
	for _, e := range entries {
		m, err := s.Catalog.Get(c.Request.Context(), e.MangaID)
		if err != nil && !errors.Is(err, ErrMangaNotFound) {
			catalogFail(c, err)
			return
		}
		items = append(items, toItem(e, m)) // a removed manga keeps an empty title
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// POST /api/library {"manga_id","status"?} → 201 LibraryItem | 400 | 404 | 409 | 503
func (s *server) addToLibrary(c *gin.Context) {
	var body struct {
		MangaID string `json:"manga_id"`
		Status  string `json:"status"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.MangaID == "" {
		fail(c, http.StatusBadRequest, "body must be {\"manga_id\": <id>, \"status\"?: <status>}")
		return
	}
	if body.Status == "" {
		body.Status = store.StatusPlanToRead
	}
	if !store.ValidStatus(body.Status) {
		fail(c, http.StatusBadRequest, "status must be plan-to-read, reading or completed")
		return
	}
	m, err := s.Catalog.Get(c.Request.Context(), body.MangaID) // must exist in the catalog
	if err != nil {
		catalogFail(c, err)
		return
	}
	e, err := s.Store.AddToLibrary(c.Request.Context(), auth.UserID(c), m.ID, body.Status)
	switch {
	case errors.Is(err, store.ErrConflict):
		fail(c, http.StatusConflict, "manga already in your library")
		return
	case err != nil:
		storeFail(c, "add to library", err)
		return
	}
	c.JSON(http.StatusCreated, toItem(e, m))
}

// PUT /api/library/:mangaId/progress {"current_chapter": n, "status"?}
// → 200 {"manga_id","current_chapter","status","version"} | 400 | 404 | 503
func (s *server) updateProgress(c *gin.Context) {
	var body struct {
		CurrentChapter *int    `json:"current_chapter"`
		Status         *string `json:"status"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.CurrentChapter == nil {
		fail(c, http.StatusBadRequest, "body must be {\"current_chapter\": <int>, \"status\"?: <status>}")
		return
	}
	status := ""
	if body.Status != nil {
		status = *body.Status
		if !store.ValidStatus(status) {
			fail(c, http.StatusBadRequest, "status must be plan-to-read, reading or completed")
			return
		}
	}
	mangaID := c.Param("mangaId")
	m, err := s.Catalog.Get(c.Request.Context(), mangaID)
	if err != nil {
		catalogFail(c, err)
		return
	}
	ch := *body.CurrentChapter
	if ch < 0 || ch > m.TotalChapters {
		fail(c, http.StatusBadRequest, "current_chapter must be between 0 and "+strconv.Itoa(m.TotalChapters))
		return
	}

	uid := auth.UserID(c)
	e, err := s.Store.UpdateProgress(c.Request.Context(), uid, mangaID, ch, status)
	switch {
	case errors.Is(err, store.ErrNotFound):
		fail(c, http.StatusNotFound, "manga is not in your library")
		return
	case errors.Is(err, store.ErrInvalidInput):
		fail(c, http.StatusBadRequest, "invalid progress")
		return
	case err != nil:
		storeFail(c, "update progress", err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"manga_id":        e.MangaID,
		"current_chapter": e.CurrentChapter,
		"status":          e.Status,
		"version":         e.Version,
	})
}
