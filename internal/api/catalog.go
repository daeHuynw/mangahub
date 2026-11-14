package api

import (
	"context"
	"errors"
	"net/http"
)

// Manga is the JSON shape returned to the TUI (contracts.md §2).
type Manga struct {
	ID            string   `json:"id"`
	Title         string   `json:"title"`
	Author        string   `json:"author"`
	Genres        []string `json:"genres"`
	Status        string   `json:"status"`
	TotalChapters int      `json:"total_chapters"`
	Description   string   `json:"description"`
}

// Catalog is everything the HTTP layer needs from the catalog service.
// GRPCCatalog is the real implementation; tests use a fake.
type Catalog interface {
	Search(ctx context.Context, query string, limit int) ([]Manga, error)
	Get(ctx context.Context, id string) (Manga, error)
	UpdateChapters(ctx context.Context, id string, total int) (Manga, error)
}

// Catalog errors, independent of gRPC so handlers and tests don't import it.
var (
	ErrMangaNotFound      = errors.New("manga not found")
	ErrInvalidArgument    = errors.New("invalid argument")
	ErrCatalogUnavailable = errors.New("catalog service unavailable")
	ErrCatalogInternal    = errors.New("catalog internal error")
)

// httpStatus maps a catalog error to the HTTP status in contracts.md §7.
func httpStatus(err error) int {
	switch {
	case errors.Is(err, ErrMangaNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrInvalidArgument):
		return http.StatusBadRequest
	case errors.Is(err, ErrCatalogUnavailable):
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}
