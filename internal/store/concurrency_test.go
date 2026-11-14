package store

import (
	"context"
	"sync"
	"testing"
)

// 20 concurrent progress updates must produce exactly 20 version bumps.
// Run with: go test -race ./internal/store
func TestUpdateProgressConcurrent(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	u := mustUser(t, s, "alice")
	s.AddToLibrary(ctx, u.ID, "one-piece", "")

	var wg sync.WaitGroup
	for i := 1; i <= 20; i++ {
		wg.Add(1)
		go func(ch int) {
			defer wg.Done()
			if _, err := s.UpdateProgress(ctx, u.ID, "one-piece", ch, ""); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	e, err := s.GetLibraryEntry(ctx, u.ID, "one-piece")
	if err != nil || e.Version != 21 {
		t.Fatalf("version = %d, want 21 (%v)", e.Version, err)
	}
}
