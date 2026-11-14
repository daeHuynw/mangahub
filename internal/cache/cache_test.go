package cache

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"mangahub/internal/api"
)

type countingCatalog struct {
	mu    sync.Mutex
	gets  int
	manga map[string]api.Manga
}

func (f *countingCatalog) Search(context.Context, string, int) ([]api.Manga, error) { return nil, nil }
func (f *countingCatalog) Get(_ context.Context, id string) (api.Manga, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gets++
	m, ok := f.manga[id]
	if !ok {
		return api.Manga{}, api.ErrMangaNotFound
	}
	return m, nil
}
func (f *countingCatalog) UpdateChapters(_ context.Context, id string, total int) (api.Manga, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := f.manga[id]
	m.TotalChapters = total
	f.manga[id] = m
	return m, nil
}

func setup(t *testing.T) (*CachedCatalog, *countingCatalog, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { rdb.Close() })
	inner := &countingCatalog{manga: map[string]api.Manga{"one-piece": {ID: "one-piece", Title: "One Piece", TotalChapters: 1130}}}
	return New(inner, rdb, 30*time.Second), inner, mr
}

func TestMissHitInvalidateMiss(t *testing.T) {
	c, inner, mr := setup(t)
	ctx := context.Background()

	if _, st, _ := c.GetWithStatus(ctx, "one-piece"); st != MISS {
		t.Fatalf("1st = %s, want MISS", st)
	}
	if _, st, _ := c.GetWithStatus(ctx, "one-piece"); st != HIT {
		t.Fatalf("2nd = %s, want HIT", st)
	}
	if inner.gets != 1 {
		t.Fatalf("catalog called %d times, want 1", inner.gets)
	}
	if ttl := mr.TTL(Key("one-piece")); ttl != 30*time.Second {
		t.Fatalf("ttl = %v", ttl)
	}

	if _, err := c.UpdateChapters(ctx, "one-piece", 1131); err != nil {
		t.Fatal(err)
	}
	m, st, _ := c.GetWithStatus(ctx, "one-piece")
	if st != MISS || m.TotalChapters != 1131 {
		t.Fatalf("after release: %s %d, want MISS 1131", st, m.TotalChapters)
	}
}

func TestTTLExpiry(t *testing.T) {
	c, _, mr := setup(t)
	ctx := context.Background()
	c.GetWithStatus(ctx, "one-piece")
	mr.FastForward(31 * time.Second)
	if _, st, _ := c.GetWithStatus(ctx, "one-piece"); st != MISS {
		t.Fatalf("after TTL = %s, want MISS", st)
	}
}

func TestNotFoundNotCached(t *testing.T) {
	c, _, mr := setup(t)
	if _, _, err := c.GetWithStatus(context.Background(), "nope"); err == nil {
		t.Fatal("expected error")
	}
	if mr.Exists(Key("nope")) {
		t.Fatal("error result was cached")
	}
}

func TestRedisDownStillServes(t *testing.T) {
	c, inner, mr := setup(t)
	mr.Close() // Redis disappears
	m, st, err := c.GetWithStatus(context.Background(), "one-piece")
	if err != nil || m.Title != "One Piece" || st != MISS {
		t.Fatalf("redis down: %+v %s %v", m, st, err)
	}
	if inner.gets != 1 {
		t.Fatalf("catalog gets = %d", inner.gets)
	}
}
