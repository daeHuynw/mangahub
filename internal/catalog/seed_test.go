package catalog

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mangahub/data"
)

func TestLoadSeed_DefaultIsEmbedded(t *testing.T) {
	b, err := LoadSeed("")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b, data.MangaSeed) {
		t.Fatal("empty path should return the embedded seed")
	}
}

func TestLoadSeed_FromFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "custom.json")
	content := []byte(`[{"id":"x","title":"X","author":"Y","genres":["action"],"status":"ongoing","total_chapters":3}]`)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := LoadSeed(path)
	if err != nil || !bytes.Equal(b, content) {
		t.Fatalf("LoadSeed(file) = %q, %v", b, err)
	}
}

func TestLoadSeed_MissingFileIsAnError(t *testing.T) {
	// No silent fallback to the embedded copy.
	if _, err := LoadSeed(filepath.Join(t.TempDir(), "nope.json")); err == nil {
		t.Fatal("want error for missing seed file")
	}
}

func TestParseSeed_Normalizes(t *testing.T) {
	items, err := ParseSeed([]byte(`[{"id":" a-b ","title":" T ","author":"A","genres":[" Action ","SCI-FI"],"status":"completed","total_chapters":0}]`))
	if err != nil {
		t.Fatal(err)
	}
	m := items[0]
	if m.ID != "a-b" || m.Title != "T" || !equal(m.Genres, []string{"action", "sci-fi"}) {
		t.Fatalf("not normalized: %+v", m)
	}
}

func TestParseSeed_Rejects(t *testing.T) {
	const ok = `"title":"T","author":"A","genres":["action"],"status":"ongoing","total_chapters":1`
	cases := map[string]string{
		"not json":        `{`,
		"empty array":     `[]`,
		"unknown field":   `[{"id":"a",` + ok + `,"total_chapter":5}]`,
		"bad id":          `[{"id":"One Piece",` + ok + `}]`,
		"duplicate id":    `[{"id":"a",` + ok + `},{"id":"a",` + ok + `}]`,
		"missing title":   `[{"id":"a","title":"","author":"A","genres":["action"],"status":"ongoing","total_chapters":1}]`,
		"missing author":  `[{"id":"a","title":"T","author":"","genres":["action"],"status":"ongoing","total_chapters":1}]`,
		"bad status":      `[{"id":"a","title":"T","author":"A","genres":["action"],"status":"hiatus","total_chapters":1}]`,
		"negative total":  `[{"id":"a","title":"T","author":"A","genres":["action"],"status":"ongoing","total_chapters":-1}]`,
		"no genres":       `[{"id":"a","title":"T","author":"A","genres":[],"status":"ongoing","total_chapters":1}]`,
		"comma in genre":  `[{"id":"a","title":"T","author":"A","genres":["a,b"],"status":"ongoing","total_chapters":1}]`,
		"duplicate genre": `[{"id":"a","title":"T","author":"A","genres":["action","Action"],"status":"ongoing","total_chapters":1}]`,
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseSeed([]byte(in)); err == nil {
				t.Fatalf("want error for %s", strings.TrimSpace(in))
			}
		})
	}
}
