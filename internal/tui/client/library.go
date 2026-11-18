package client

import (
	"context"
	"net/http"
	"net/url"
)

// Library statuses (contracts.md §2).
const (
	StatusPlanToRead = "plan-to-read"
	StatusReading    = "reading"
	StatusCompleted  = "completed"
)

// LibraryStatuses lists the statuses in the order the UI offers them.
var LibraryStatuses = []string{StatusPlanToRead, StatusReading, StatusCompleted}

// LibraryItem is one row of GET /api/library.
type LibraryItem struct {
	MangaID        string `json:"manga_id"`
	Title          string `json:"title"`
	Status         string `json:"status"`
	CurrentChapter int    `json:"current_chapter"`
	TotalChapters  int    `json:"total_chapters"`
	UpdatedAt      string `json:"updated_at"`
	Version        int64  `json:"version"`
}

// Progress is what PUT /api/library/:id/progress returns, and also what a TCP
// progress_updated event carries. Version grows by one on every update, so
// the UI can ignore anything older than what it already shows.
type Progress struct {
	MangaID        string `json:"manga_id"`
	CurrentChapter int    `json:"current_chapter"`
	Status         string `json:"status"`
	Version        int64  `json:"version"`
}

// ListLibrary calls GET /api/library (the logged-in user's rows only; the
// backend takes the user from the JWT). Errors: 401, 503.
func (c *Client) ListLibrary(ctx context.Context) ([]LibraryItem, error) {
	var resp struct {
		Items []LibraryItem `json:"items"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/library", nil, &resp, nil); err != nil {
		return nil, err
	}
	if resp.Items == nil {
		resp.Items = []LibraryItem{}
	}
	return resp.Items, nil
}

// AddToLibrary calls POST /api/library. An empty status means plan-to-read.
// Errors: 400, 401, 404 (unknown manga), 409 (already added), 503.
func (c *Client) AddToLibrary(ctx context.Context, mangaID, status string) (LibraryItem, error) {
	body := map[string]string{"manga_id": mangaID}
	if status != "" {
		body["status"] = status
	}
	var item LibraryItem
	err := c.do(ctx, http.MethodPost, "/api/library", body, &item, nil)
	return item, err
}

// UpdateProgress calls PUT /api/library/:mangaId/progress. status may be ""
// to keep the current one. The X-Session-ID header (set by SetSessionID) tells
// the backend not to push this change back to our own TCP session.
// Errors: 400 (chapter outside 0..total, bad status), 401, 404 (not in
// library), 503.
func (c *Client) UpdateProgress(ctx context.Context, mangaID string, chapter int, status string) (Progress, error) {
	body := map[string]any{"current_chapter": chapter}
	if status != "" {
		body["status"] = status
	}
	var p Progress
	err := c.do(ctx, http.MethodPut, "/api/library/"+url.PathEscape(mangaID)+"/progress", body, &p, nil)
	return p, err
}
