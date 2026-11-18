package tui

import (
	"testing"

	"mangahub/internal/tui/client"
)

func lib() *libraryState {
	s := &libraryState{}
	s.set([]client.LibraryItem{
		{MangaID: "naruto", Title: "Naruto", Status: "reading", CurrentChapter: 10, TotalChapters: 700, Version: 3},
		{MangaID: "akira", Title: "Akira", Status: "plan-to-read", TotalChapters: 120, Version: 1},
	})
	return s
}

func TestLibraryState_SortedByTitle(t *testing.T) {
	s := lib()
	if s.items[0].MangaID != "akira" || s.items[1].MangaID != "naruto" {
		t.Fatalf("order = %s, %s", s.items[0].MangaID, s.items[1].MangaID)
	}
	s.add(client.LibraryItem{MangaID: "bleach", Title: "bleach", Version: 1})
	if s.items[1].MangaID != "bleach" {
		t.Fatalf("add did not keep case-insensitive order: %+v", s.items)
	}
}

func TestLibraryState_ApplyOnlyNewerVersions(t *testing.T) {
	s := lib()
	if !s.apply(client.Progress{MangaID: "naruto", CurrentChapter: 12, Status: "reading", Version: 4}) {
		t.Fatal("v4 should apply over v3")
	}
	// Same version again (our own HTTP answer + a duplicate event): ignored.
	if s.apply(client.Progress{MangaID: "naruto", CurrentChapter: 99, Version: 4}) {
		t.Fatal("equal version applied")
	}
	// Late, older event arriving after a newer one: ignored.
	if s.apply(client.Progress{MangaID: "naruto", CurrentChapter: 11, Version: 2}) {
		t.Fatal("older version applied")
	}
	if it, _ := s.get("naruto"); it.CurrentChapter != 12 || it.Version != 4 {
		t.Fatalf("row = %+v, want chapter 12 v4", it)
	}
	// Unknown manga: caller must reload the library.
	if s.apply(client.Progress{MangaID: "nope", Version: 9}) {
		t.Fatal("unknown manga applied")
	}
	// Empty status keeps the old one.
	s.apply(client.Progress{MangaID: "naruto", CurrentChapter: 13, Version: 5})
	if it, _ := s.get("naruto"); it.Status != "reading" {
		t.Fatalf("status = %q", it.Status)
	}
}

func TestLibraryState_SetTotal(t *testing.T) {
	s := lib()
	if !s.setTotal("naruto", 701) || s.setTotal("naruto", 701) || s.setTotal("naruto", 5) || s.setTotal("nope", 9) {
		t.Fatal("setTotal should only raise the count of a known manga")
	}
}

func TestParseChapter(t *testing.T) {
	cases := []struct {
		in    string
		total int
		want  int
		ok    bool
	}{
		{"12", 700, 12, true},
		{" 0 ", 700, 0, true},
		{"700", 700, 700, true},
		{"701", 700, 0, false},
		{"-1", 700, 0, false},
		{"abc", 700, 0, false},
		{"", 700, 0, false},
		{"1.5", 700, 0, false},
	}
	for _, tc := range cases {
		n, msg := parseChapter(tc.in, tc.total)
		if (msg == "") != tc.ok || (tc.ok && n != tc.want) {
			t.Errorf("parseChapter(%q, %d) = %d, %q", tc.in, tc.total, n, msg)
		}
	}
}
