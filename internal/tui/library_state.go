package tui

import (
	"sort"
	"strconv"
	"strings"

	"mangahub/internal/tui/client"
)

// libraryState is the TUI's copy of the user's library. It has no tview or
// network code so the ordering rule can be tested on its own.
//
// Two sources change it: our own HTTP responses and TCP progress_updated
// events from other sessions. They can arrive in any order, so every change
// carries the server's version and apply() keeps only the newest one.
type libraryState struct {
	items []client.LibraryItem // sorted by title
}

// set replaces everything (after GET /api/library).
func (s *libraryState) set(items []client.LibraryItem) {
	s.items = append([]client.LibraryItem(nil), items...)
	s.sort()
}

// add inserts or replaces one row (after POST /api/library).
func (s *libraryState) add(it client.LibraryItem) {
	if i := s.index(it.MangaID); i >= 0 {
		s.items[i] = it
	} else {
		s.items = append(s.items, it)
	}
	s.sort()
}

// apply merges a progress change. It returns false when the manga is not in
// the list (reload needed) or when p is not newer than what is stored
// (duplicate or late event), in which case nothing changes.
func (s *libraryState) apply(p client.Progress) bool {
	i := s.index(p.MangaID)
	if i < 0 || p.Version <= s.items[i].Version {
		return false
	}
	it := &s.items[i]
	it.CurrentChapter, it.Version = p.CurrentChapter, p.Version
	if p.Status != "" {
		it.Status = p.Status
	}
	return true
}

// setTotal updates total_chapters after a new-chapter alert (UDP).
func (s *libraryState) setTotal(mangaID string, total int) bool {
	i := s.index(mangaID)
	if i < 0 || total <= s.items[i].TotalChapters {
		return false
	}
	s.items[i].TotalChapters = total
	return true
}

func (s *libraryState) get(mangaID string) (client.LibraryItem, bool) {
	if i := s.index(mangaID); i >= 0 {
		return s.items[i], true
	}
	return client.LibraryItem{}, false
}

func (s *libraryState) index(mangaID string) int {
	for i := range s.items {
		if s.items[i].MangaID == mangaID {
			return i
		}
	}
	return -1
}

func (s *libraryState) sort() {
	sort.SliceStable(s.items, func(i, j int) bool {
		return strings.ToLower(s.items[i].Title) < strings.ToLower(s.items[j].Title)
	})
}

// parseChapter checks the chapter typed in the update form. The backend does
// the real check (it asks the catalog for total_chapters); this only gives
// instant feedback for obvious mistakes.
func parseChapter(text string, total int) (int, string) {
	n, err := strconv.Atoi(strings.TrimSpace(text))
	if err != nil {
		return 0, "Chapter must be a whole number."
	}
	if n < 0 || n > total {
		return 0, "Chapter must be between 0 and " + strconv.Itoa(total) + "."
	}
	return n, ""
}
