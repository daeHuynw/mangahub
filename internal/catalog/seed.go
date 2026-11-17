package catalog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"

	"mangahub/data"
)

// SeedEnv names the environment variable that overrides the embedded seed.
const SeedEnv = "CATALOG_SEED"

// slugRe matches lowercase slugs such as "one-piece" or "20th-century-boys".
// Used for manga ids and genre names.
var slugRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// LoadSeed returns the seed JSON. An empty path means the copy embedded in the
// binary (data.MangaSeed). A non-empty path is read from disk, and a read
// error is returned rather than silently falling back to the embedded copy:
// if the operator asked for a specific file, using a different one would hide
// the mistake.
func LoadSeed(path string) ([]byte, error) {
	if path == "" {
		return data.MangaSeed, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read seed %s: %w", path, err)
	}
	return b, nil
}

// ParseSeed decodes and validates a JSON array of manga. Unknown fields are
// rejected so a typo such as "total_chapter" is caught instead of becoming 0.
func ParseSeed(b []byte) ([]Manga, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var items []Manga
	if err := dec.Decode(&items); err != nil {
		return nil, fmt.Errorf("decode seed: %w", err)
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("seed is empty")
	}
	seen := make(map[string]bool, len(items))
	for i := range items {
		m := &items[i]
		normalize(m)
		if err := validateManga(*m); err != nil {
			return nil, fmt.Errorf("seed record %d (%q): %w", i, m.ID, err)
		}
		if seen[m.ID] {
			return nil, fmt.Errorf("seed record %d: duplicate id %q", i, m.ID)
		}
		seen[m.ID] = true
	}
	return items, nil
}

// normalize trims whitespace and lowercases genres so "Action " and "action"
// are the same genre.
func normalize(m *Manga) {
	m.ID = strings.TrimSpace(m.ID)
	m.Title = strings.TrimSpace(m.Title)
	m.Author = strings.TrimSpace(m.Author)
	m.Status = strings.TrimSpace(m.Status)
	m.Description = strings.TrimSpace(m.Description)
	for i, g := range m.Genres {
		m.Genres[i] = strings.ToLower(strings.TrimSpace(g))
	}
}

func validateManga(m Manga) error {
	switch {
	case !slugRe.MatchString(m.ID):
		return fmt.Errorf("id must be a lowercase slug like \"one-piece\"")
	case m.Title == "":
		return fmt.Errorf("title is required")
	case m.Author == "":
		return fmt.Errorf("author is required")
	case m.Status != StatusOngoing && m.Status != StatusCompleted:
		return fmt.Errorf("status must be %q or %q", StatusOngoing, StatusCompleted)
	case m.TotalChapters < 0:
		return fmt.Errorf("total_chapters must be >= 0")
	case len(m.Genres) == 0:
		return fmt.Errorf("at least one genre is required")
	}
	seen := map[string]bool{}
	for _, g := range m.Genres {
		if !slugRe.MatchString(g) {
			return fmt.Errorf("genre %q must be a lowercase slug like \"sci-fi\"", g)
		}
		if seen[g] {
			return fmt.Errorf("duplicate genre %q", g)
		}
		seen[g] = true
	}
	return nil
}
