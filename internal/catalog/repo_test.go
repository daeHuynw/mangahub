package catalog

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"mangahub/data"
)

// newSeededRepo opens a fresh file-backed catalog (WAL, like production) and
// imports the embedded seed.
func newSeededRepo(t *testing.T) (*Repo, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "catalog.db")
	r, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { r.Close() })
	if _, err := r.SeedIfEmpty(context.Background(), data.MangaSeed); err != nil {
		t.Fatalf("SeedIfEmpty: %v", err)
	}
	return r, path
}

func TestSeedIfEmpty_ImportsOnlyOnce(t *testing.T) {
	ctx := context.Background()
	r, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	n, err := r.SeedIfEmpty(ctx, data.MangaSeed)
	if err != nil {
		t.Fatalf("first seed: %v", err)
	}
	if n < 30 {
		t.Fatalf("seed inserted %d manga, want >= 30", n)
	}
	again, err := r.SeedIfEmpty(ctx, data.MangaSeed)
	if err != nil || again != 0 {
		t.Fatalf("second seed = %d, %v; want 0, nil", again, err)
	}
	if c, _ := r.Count(ctx); c != n {
		t.Fatalf("count = %d, want %d (no duplicates after second seed)", c, n)
	}
}

func TestSeedIfEmpty_DoesNotTouchExistingCatalog(t *testing.T) {
	ctx := context.Background()
	r, _ := newSeededRepo(t)
	if _, err := r.UpdateChapters(ctx, "naruto", 701); err != nil {
		t.Fatal(err)
	}
	// Re-seeding a non-empty catalog must not reset the released chapter.
	if _, err := r.SeedIfEmpty(ctx, data.MangaSeed); err != nil {
		t.Fatal(err)
	}
	m, _ := r.Get(ctx, "naruto")
	if m.TotalChapters != 701 {
		t.Fatalf("naruto total = %d after re-seed, want 701", m.TotalChapters)
	}
}

func TestSeedIfEmpty_InvalidSeedLeavesCatalogEmpty(t *testing.T) {
	ctx := context.Background()
	r, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	bad := []byte(`[{"id":"ok-one","title":"A","author":"B","genres":["action"],"status":"ongoing","total_chapters":1},
	                {"id":"bad","title":"C","author":"D","genres":["action"],"status":"paused","total_chapters":1}]`)
	if _, err := r.SeedIfEmpty(ctx, bad); err == nil {
		t.Fatal("want error for invalid status")
	}
	if c, _ := r.Count(ctx); c != 0 {
		t.Fatalf("count = %d after failed seed, want 0 (all or nothing)", c)
	}
}

func TestEmbeddedSeed_MeetsProjectMinimum(t *testing.T) {
	items, err := ParseSeed(data.MangaSeed)
	if err != nil {
		t.Fatalf("embedded seed invalid: %v", err)
	}
	genres := map[string]bool{}
	for _, m := range items {
		for _, g := range m.Genres {
			genres[g] = true
		}
	}
	if len(items) < 30 || len(genres) < 4 {
		t.Fatalf("seed has %d manga and %d genres; spec needs >= 30 and >= 4", len(items), len(genres))
	}
	// Ids used by member A's smoke test and test log must exist.
	for _, id := range []string{"one-piece", "naruto"} {
		found := false
		for _, m := range items {
			found = found || m.ID == id
		}
		if !found {
			t.Errorf("seed is missing %q", id)
		}
	}
}

func TestPersistence_SurvivesReopen(t *testing.T) {
	ctx := context.Background()
	r, path := newSeededRepo(t)
	if _, err := r.UpdateChapters(ctx, "one-piece", 1131); err != nil {
		t.Fatal(err)
	}
	r.Close()

	r2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Close()
	m, err := r2.Get(ctx, "one-piece")
	if err != nil || m.TotalChapters != 1131 {
		t.Fatalf("after reopen: %+v, %v; want total 1131", m, err)
	}
}

func TestSearch(t *testing.T) {
	ctx := context.Background()
	r, _ := newSeededRepo(t)
	total, _ := r.Count(ctx)

	cases := []struct {
		name    string
		query   string
		limit   int
		wantLen int    // -1 = don't check
		wantID  string // must be present ("" = don't check)
	}{
		{"title substring", "piece", 0, 1, "one-piece"},
		{"case insensitive", "PIECE", 0, 1, "one-piece"},
		{"author", "urasawa", 0, 2, "monster"},
		{"genre exact", "sports", 0, 3, "haikyu"},
		{"surrounding spaces", "  naruto  ", 0, 1, "naruto"},
		{"empty lists all", "", 0, total, ""},
		{"limit", "", 5, 5, ""},
		{"no result", "zzzzzz", 0, 0, ""},
		{"percent is literal", "100%", 0, 0, ""},
		{"underscore is literal", "one_piece", 0, 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := r.Search(ctx, tc.query, tc.limit)
			if err != nil {
				t.Fatal(err)
			}
			if got == nil {
				t.Fatal("Search returned nil slice; want empty non-nil (JSON [] not null)")
			}
			if tc.wantLen >= 0 && len(got) != tc.wantLen {
				t.Fatalf("len = %d, want %d: %v", len(got), tc.wantLen, ids(got))
			}
			if tc.wantID != "" && !contains(got, tc.wantID) {
				t.Fatalf("%q not in %v", tc.wantID, ids(got))
			}
		})
	}
}

func TestSearch_OrderedByTitleAndLimitCapped(t *testing.T) {
	r, _ := newSeededRepo(t)
	got, err := r.Search(context.Background(), "", MaxLimit+1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) > MaxLimit {
		t.Fatalf("len = %d, want <= %d", len(got), MaxLimit)
	}
	for i := 1; i < len(got); i++ {
		if lower(got[i-1].Title) > lower(got[i].Title) {
			t.Fatalf("not sorted: %q before %q", got[i-1].Title, got[i].Title)
		}
	}
}

func TestGet(t *testing.T) {
	ctx := context.Background()
	r, _ := newSeededRepo(t)

	m, err := r.Get(ctx, "one-piece")
	if err != nil {
		t.Fatal(err)
	}
	if m.Title != "One Piece" || m.Author == "" || m.Status != StatusOngoing || m.TotalChapters != 1130 || m.Description == "" {
		t.Fatalf("unexpected record: %+v", m)
	}
	// Genres come back in seed order.
	if want := []string{"action", "adventure", "comedy"}; !equal(m.Genres, want) {
		t.Fatalf("genres = %v, want %v", m.Genres, want)
	}

	if _, err := r.Get(ctx, ""); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("empty id: err = %v, want ErrInvalidArgument", err)
	}
	if _, err := r.Get(ctx, "does-not-exist"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown id: err = %v, want ErrNotFound", err)
	}
}

func TestUpdateChapters(t *testing.T) {
	ctx := context.Background()
	r, _ := newSeededRepo(t)

	m, err := r.UpdateChapters(ctx, "one-piece", 1131)
	if err != nil || m.TotalChapters != 1131 || m.Title != "One Piece" {
		t.Fatalf("raise: %+v, %v", m, err)
	}

	cases := []struct {
		name  string
		id    string
		total int
		want  error
	}{
		{"equal is rejected", "one-piece", 1131, ErrInvalidArgument},
		{"decrease is rejected", "one-piece", 1, ErrInvalidArgument},
		{"zero is rejected", "one-piece", 0, ErrInvalidArgument},
		{"negative is rejected", "one-piece", -5, ErrInvalidArgument},
		{"empty id", "", 10, ErrInvalidArgument},
		{"unknown id", "does-not-exist", 10, ErrNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := r.UpdateChapters(ctx, tc.id, tc.total); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
	// Rejected updates must not have changed the stored value.
	if m, _ := r.Get(ctx, "one-piece"); m.TotalChapters != 1131 {
		t.Fatalf("total = %d after rejected updates, want 1131", m.TotalChapters)
	}
}

// Concurrent releases with different values: the final count must be the
// largest one, never a smaller value written later (no lost update).
func TestUpdateChapters_ConcurrentNeverGoesBackwards(t *testing.T) {
	ctx := context.Background()
	r, _ := newSeededRepo(t)
	const base, n = 700, 20

	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := 0
	for i := 1; i <= n; i++ {
		wg.Add(1)
		go func(total int) {
			defer wg.Done()
			_, err := r.UpdateChapters(ctx, "naruto", total)
			switch {
			case err == nil:
				mu.Lock()
				ok++
				mu.Unlock()
			case !errors.Is(err, ErrInvalidArgument):
				t.Errorf("total %d: unexpected error %v", total, err)
			}
		}(base + i)
	}
	wg.Wait()

	m, _ := r.Get(ctx, "naruto")
	if m.TotalChapters != base+n {
		t.Fatalf("final total = %d, want %d", m.TotalChapters, base+n)
	}
	if ok < 1 || ok > n {
		t.Fatalf("successful updates = %d", ok)
	}
}

// ---------------------------------------------------------------- helpers

func ids(ms []Manga) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.ID
	}
	return out
}

func contains(ms []Manga, id string) bool {
	for _, m := range ms {
		if m.ID == id {
			return true
		}
	}
	return false
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func lower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}
