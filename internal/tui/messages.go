package tui

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/rivo/tview"

	"mangahub/internal/tui/client"
)

// This file holds the pure (tview-free, network-free) logic of the screens so
// it can be unit-tested directly: error wording, input validation, genre
// filtering and text formatting.

// friendlyError turns a client error into a sentence for the status bar.
// The HTTP codes follow contracts.md §3.
func friendlyError(err error, backend string) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, client.ErrUnreachable) {
		return fmt.Sprintf("Cannot reach the backend at %s. Is cmd/backend running?", backend)
	}
	var ae *client.APIError
	if !errors.As(err, &ae) {
		return "Unexpected error: " + err.Error()
	}
	switch ae.Status {
	case http.StatusBadRequest:
		return "Invalid request: " + ae.Message
	case http.StatusUnauthorized:
		return "Your session is not valid any more. Please log in again."
	case http.StatusNotFound:
		return "Manga not found (it may have been removed from the catalog)."
	case http.StatusConflict:
		return "That username is already taken."
	case http.StatusServiceUnavailable:
		return "The catalog service is unavailable right now. Try again in a moment."
	default:
		return fmt.Sprintf("Unexpected error (HTTP %d): %s", ae.Status, ae.Message)
	}
}

var usernameRe = regexp.MustCompile(`^[A-Za-z0-9]{3,32}$`)

// validateCredentials mirrors the backend rules (contracts.md §3: username
// 3–32 letters/digits, password 6–72) so obvious mistakes are reported
// instantly. The backend still validates; this is only for feedback.
func validateCredentials(username, password string) string {
	switch {
	case username == "" || password == "":
		return "Enter a username and a password."
	case !usernameRe.MatchString(username):
		return "Username must be 3-32 letters or digits."
	case len(password) < 6 || len(password) > 72:
		return "Password must be 6-72 characters."
	}
	return ""
}

// allGenres is the first entry of the genre drop-down.
const allGenres = "All genres"

// genresOf returns the sorted, de-duplicated genres of items.
func genresOf(items []client.Manga) []string {
	set := map[string]bool{}
	for _, m := range items {
		for _, g := range m.Genres {
			set[g] = true
		}
	}
	out := make([]string, 0, len(set))
	for g := range set {
		out = append(out, g)
	}
	sort.Strings(out)
	return out
}

// filterByGenre keeps the items that have genre ("" or allGenres keeps all).
func filterByGenre(items []client.Manga, genre string) []client.Manga {
	if genre == "" || genre == allGenres {
		return items
	}
	out := []client.Manga{}
	for _, m := range items {
		for _, g := range m.Genres {
			if g == genre {
				out = append(out, m)
				break
			}
		}
	}
	return out
}

// listLabel is one line of the manga list. Data is escaped so a title like
// "[red]" cannot inject tview color tags.
func listLabel(m client.Manga) string {
	return fmt.Sprintf("%s [gray](%s, %d ch)", tview.Escape(m.Title), m.Status, m.TotalChapters)
}

// detailsText renders the details panel (all six required fields).
func detailsText(m client.Manga, cache string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[::b]%s[::-]\n\n", tview.Escape(m.Title))
	fmt.Fprintf(&b, "[yellow]Author:[-]    %s\n", tview.Escape(m.Author))
	fmt.Fprintf(&b, "[yellow]Genres:[-]    %s\n", tview.Escape(strings.Join(m.Genres, ", ")))
	fmt.Fprintf(&b, "[yellow]Status:[-]    %s\n", tview.Escape(m.Status))
	fmt.Fprintf(&b, "[yellow]Chapters:[-]  %d\n", m.TotalChapters)
	fmt.Fprintf(&b, "[yellow]ID:[-]        %s\n", tview.Escape(m.ID))
	if cache != "" {
		fmt.Fprintf(&b, "[yellow]X-Cache:[-]   %s\n", tview.Escape(cache))
	}
	fmt.Fprintf(&b, "\n%s\n", tview.Escape(m.Description))
	return b.String()
}
