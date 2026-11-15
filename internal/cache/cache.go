// Package cache implements the optional F07 Redis cache-aside layer in front
// of the gRPC catalog. The catalog SQLite database stays the source of truth:
// Redis only holds copies of GET /api/manga/:id results for TTL.
package cache

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"time"

	"github.com/redis/go-redis/v9"

	"mangahub/internal/api"
)

const (
	HIT  = "HIT"
	MISS = "MISS"
	// RedisTimeout bounds every Redis call so a slow/dead Redis degrades to a
	// MISS instead of slowing every request.
	RedisTimeout = 200 * time.Millisecond
)

// CachedCatalog wraps any api.Catalog. Search is not cached (results change
// with the query); Get is cached; UpdateChapters invalidates.
type CachedCatalog struct {
	inner api.Catalog
	rdb   *redis.Client
	ttl   time.Duration
}

var _ api.Catalog = (*CachedCatalog)(nil)
var _ api.CacheReporter = (*CachedCatalog)(nil)

func New(inner api.Catalog, rdb *redis.Client, ttl time.Duration) *CachedCatalog {
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	return &CachedCatalog{inner: inner, rdb: rdb, ttl: ttl}
}

func Key(id string) string { return "manga:" + id }

func (c *CachedCatalog) Search(ctx context.Context, q string, limit int) ([]api.Manga, error) {
	return c.inner.Search(ctx, q, limit)
}

func (c *CachedCatalog) Get(ctx context.Context, id string) (api.Manga, error) {
	m, _, err := c.GetWithStatus(ctx, id)
	return m, err
}

// GetWithStatus: HIT → value from Redis; MISS → gRPC, then store in Redis.
// If Redis is down the request still succeeds from gRPC (reported as MISS).
func (c *CachedCatalog) GetWithStatus(ctx context.Context, id string) (api.Manga, string, error) {
	rctx, cancel := context.WithTimeout(ctx, RedisTimeout)
	raw, err := c.rdb.Get(rctx, Key(id)).Bytes()
	cancel()
	if err == nil {
		var m api.Manga
		if json.Unmarshal(raw, &m) == nil {
			return m, HIT, nil
		}
	} else if !errors.Is(err, redis.Nil) {
		log.Printf("cache: redis get %s: %v (falling back to catalog)", id, err)
	}

	m, err := c.inner.Get(ctx, id)
	if err != nil {
		return api.Manga{}, MISS, err // errors (404/503) are never cached
	}
	if b, err := json.Marshal(m); err == nil {
		wctx, cancel := context.WithTimeout(ctx, RedisTimeout)
		if err := c.rdb.Set(wctx, Key(id), b, c.ttl).Err(); err != nil {
			log.Printf("cache: redis set %s: %v", id, err)
		}
		cancel()
	}
	return m, MISS, nil
}

// UpdateChapters writes through gRPC first; only on success the stale key is
// deleted, so the next read is a MISS that returns the new chapter count.
func (c *CachedCatalog) UpdateChapters(ctx context.Context, id string, total int) (api.Manga, error) {
	m, err := c.inner.UpdateChapters(ctx, id, total)
	if err != nil {
		return m, err
	}
	c.Invalidate(ctx, id)
	return m, nil
}

func (c *CachedCatalog) Invalidate(ctx context.Context, id string) {
	dctx, cancel := context.WithTimeout(ctx, RedisTimeout)
	defer cancel()
	if err := c.rdb.Del(dctx, Key(id)).Err(); err != nil {
		// Worst case the old value lives until TTL expires (≤ 30s staleness).
		log.Printf("cache: redis del %s: %v", id, err)
	}
}
