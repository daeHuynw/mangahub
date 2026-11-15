package store

import (
	"context"
	"errors"
	"testing"
)

func TestAddToLibraryDuplicate(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	u := mustUser(t, s, "alice")
	e, err := s.AddToLibrary(ctx, u.ID, "one-piece", "")
	if err != nil {
		t.Fatal(err)
	}
	if e.Status != StatusPlanToRead || e.Version != 1 {
		t.Fatalf("unexpected entry %+v", e)
	}
	if _, err := s.AddToLibrary(ctx, u.ID, "one-piece", ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate add: got %v, want ErrConflict", err)
	}
	if _, err := s.AddToLibrary(ctx, u.ID, "naruto", "dropped"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("bad status: got %v, want ErrInvalidInput", err)
	}
}

func TestUpdateProgressBumpsVersion(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	u := mustUser(t, s, "alice")
	s.AddToLibrary(ctx, u.ID, "one-piece", "")

	e, err := s.UpdateProgress(ctx, u.ID, "one-piece", 10, StatusReading)
	if err != nil || e.Version != 2 || e.CurrentChapter != 10 || e.Status != StatusReading {
		t.Fatalf("first update: %+v %v", e, err)
	}
	e, err = s.UpdateProgress(ctx, u.ID, "one-piece", 12, "")
	if err != nil || e.Version != 3 || e.Status != StatusReading {
		t.Fatalf("second update keeps status: %+v %v", e, err)
	}
	if _, err := s.UpdateProgress(ctx, u.ID, "one-piece", -1, ""); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("negative chapter: got %v", err)
	}
	if _, err := s.UpdateProgress(ctx, u.ID, "naruto", 1, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("not in library: got %v", err)
	}
}

func TestUpdateStatus(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	u := mustUser(t, s, "alice")
	s.AddToLibrary(ctx, u.ID, "one-piece", "")
	e, err := s.UpdateStatus(ctx, u.ID, "one-piece", StatusCompleted)
	if err != nil || e.Status != StatusCompleted || e.Version != 2 {
		t.Fatalf("update status: %+v %v", e, err)
	}
	if _, err := s.UpdateStatus(ctx, u.ID, "one-piece", "dropped"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("bad status: got %v", err)
	}
}

func TestLibraryIsolatedPerUser(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	alice := mustUser(t, s, "alice")
	bob := mustUser(t, s, "bob")
	s.AddToLibrary(ctx, alice.ID, "one-piece", "")

	items, err := s.ListLibrary(ctx, bob.ID)
	if err != nil || len(items) != 0 {
		t.Fatalf("bob sees %d items (%v)", len(items), err)
	}
	if _, err := s.UpdateProgress(ctx, bob.ID, "one-piece", 5, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("bob updating alice's entry: got %v, want ErrNotFound", err)
	}
}

func TestListLibraryNewestFirst(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	u := mustUser(t, s, "alice")
	s.AddToLibrary(ctx, u.ID, "a", "")
	s.AddToLibrary(ctx, u.ID, "b", "")
	items, err := s.ListLibrary(ctx, u.ID)
	if err != nil || len(items) != 2 || items[0].MangaID != "b" {
		t.Fatalf("got %+v %v", items, err)
	}
}
