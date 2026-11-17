// Package catalog owns the manga catalog: the SQLite database catalog.db and
// (in server.go, stage B2) the gRPC CatalogService implementation.
//
// Data ownership: only the Catalog Service process opens catalog.db. The Gin
// backend never reads it; it asks for manga data over gRPC.
package catalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver, registered as "sqlite"
)

// Sentinel errors. The gRPC layer maps them to status codes with errors.Is:
// ErrNotFound → codes.NotFound, ErrInvalidArgument → codes.InvalidArgument,
// anything else → codes.Internal.
var (
	ErrNotFound        = errors.New("manga not found")
	ErrInvalidArgument = errors.New("invalid argument")
)

// Manga statuses accepted by the catalog (contracts.md §2).
const (
	StatusOngoing   = "ongoing"
	StatusCompleted = "completed"
)

// Search limits. limit <= 0 means DefaultLimit; larger values are capped.
const (
	DefaultLimit = 50
	MaxLimit     = 200
)

// Manga is one catalog record. The JSON tags match manga_seed.json and the
// contract's Manga object.
type Manga struct {
	ID            string   `json:"id"`
	Title         string   `json:"title"`
	Author        string   `json:"author"`
	Genres        []string `json:"genres"`
	Status        string   `json:"status"`
	TotalChapters int      `json:"total_chapters"`
	Description   string   `json:"description"`
}

// Genres live in their own table (one row per manga/genre pair) instead of a
// comma-separated column, so the database itself prevents duplicates and a
// genre search can use an index. position keeps the original display order.
const schema = `
CREATE TABLE IF NOT EXISTS manga (
	id             TEXT    PRIMARY KEY,
	title          TEXT    NOT NULL CHECK (title <> ''),
	author         TEXT    NOT NULL,
	status         TEXT    NOT NULL CHECK (status IN ('ongoing','completed')),
	total_chapters INTEGER NOT NULL CHECK (total_chapters >= 0),
	description    TEXT    NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS manga_genres (
	manga_id TEXT    NOT NULL REFERENCES manga(id) ON DELETE CASCADE,
	genre    TEXT    NOT NULL,
	position INTEGER NOT NULL,
	PRIMARY KEY (manga_id, genre)
);
CREATE INDEX IF NOT EXISTS idx_manga_genres_genre ON manga_genres(genre);
`

// selectManga returns every column plus the genres folded into one
// comma-separated string in their original order (genre names never contain
// commas: validateManga enforces a slug format).
const selectManga = `
SELECT m.id, m.title, m.author, m.status, m.total_chapters, m.description,
       COALESCE((SELECT group_concat(g.genre, ',' ORDER BY g.position)
                 FROM manga_genres g WHERE g.manga_id = m.id), '')
FROM manga m`

// Repo is the catalog's data access layer. It is safe for concurrent use.
type Repo struct {
	db *sql.DB
}

// Open opens (or creates) the SQLite file and applies the schema. path may be
// ":memory:" for tests. DSN conventions are the same as internal/store.
func Open(path string) (*Repo, error) {
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	if path != ":memory:" {
		q.Add("_pragma", "journal_mode(WAL)")
	}
	db, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// One connection: SQLite has a single writer anyway, so serialising in Go
	// avoids SQLITE_BUSY and keeps a ":memory:" database shared across calls.
	db.SetMaxOpenConns(1)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	if _, err := db.ExecContext(ctx, schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("create schema: %w", err)
	}
	return &Repo{db: db}, nil
}

// Close closes the database.
func (r *Repo) Close() error { return r.db.Close() }

// Count returns the number of manga in the catalog.
func (r *Repo) Count(ctx context.Context) (int, error) {
	var n int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM manga`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count manga: %w", err)
	}
	return n, nil
}

// SeedIfEmpty imports the JSON seed only when the manga table is empty and
// returns how many records were inserted (0 if the catalog already had data).
// The whole seed is validated first and inserted in one transaction, so a bad
// file never leaves a half-filled catalog.
func (r *Repo) SeedIfEmpty(ctx context.Context, seed []byte) (int, error) {
	items, err := ParseSeed(seed)
	if err != nil {
		return 0, err
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin seed: %w", err)
	}
	defer tx.Rollback() // no-op after Commit

	// Counting inside the transaction makes check-then-insert atomic.
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM manga`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count manga: %w", err)
	}
	if n > 0 {
		return 0, nil
	}

	insManga, err := tx.PrepareContext(ctx,
		`INSERT INTO manga (id, title, author, status, total_chapters, description) VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, fmt.Errorf("prepare insert manga: %w", err)
	}
	defer insManga.Close()
	insGenre, err := tx.PrepareContext(ctx,
		`INSERT INTO manga_genres (manga_id, genre, position) VALUES (?, ?, ?)`)
	if err != nil {
		return 0, fmt.Errorf("prepare insert genre: %w", err)
	}
	defer insGenre.Close()

	for _, m := range items {
		if _, err := insManga.ExecContext(ctx, m.ID, m.Title, m.Author, m.Status, m.TotalChapters, m.Description); err != nil {
			return 0, fmt.Errorf("insert manga %q: %w", m.ID, err)
		}
		for i, g := range m.Genres {
			if _, err := insGenre.ExecContext(ctx, m.ID, g, i); err != nil {
				return 0, fmt.Errorf("insert genre %q for %q: %w", g, m.ID, err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit seed: %w", err)
	}
	return len(items), nil
}

// Search returns manga whose title or author contains query
// (case-insensitive for ASCII), or that have a genre equal to query, ordered
// by title. An empty query lists the whole catalog. No match returns an empty,
// non-nil slice: "no results" is a valid answer, not an error.
func (r *Repo) Search(ctx context.Context, query string, limit int) ([]Manga, error) {
	if limit <= 0 {
		limit = DefaultLimit
	}
	if limit > MaxLimit {
		limit = MaxLimit
	}
	query = strings.TrimSpace(query)

	var (
		rows *sql.Rows
		err  error
	)
	if query == "" {
		rows, err = r.db.QueryContext(ctx, selectManga+` ORDER BY m.title COLLATE NOCASE LIMIT ?`, limit)
	} else {
		// Escape LIKE wildcards so "100%" or "a_b" are matched literally.
		pattern := "%" + escapeLike(query) + "%"
		rows, err = r.db.QueryContext(ctx, selectManga+`
WHERE m.title LIKE ? ESCAPE '\' OR m.author LIKE ? ESCAPE '\'
   OR EXISTS (SELECT 1 FROM manga_genres g WHERE g.manga_id = m.id AND g.genre = lower(?))
ORDER BY m.title COLLATE NOCASE LIMIT ?`, pattern, pattern, query, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("search manga: %w", err)
	}
	defer rows.Close()

	out := []Manga{}
	for rows.Next() {
		m, err := scanManga(rows)
		if err != nil {
			return nil, fmt.Errorf("scan manga: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("search manga: %w", err)
	}
	return out, nil
}

// Get returns one manga by id.
// Errors: ErrInvalidArgument (empty id), ErrNotFound.
func (r *Repo) Get(ctx context.Context, id string) (Manga, error) {
	if strings.TrimSpace(id) == "" {
		return Manga{}, fmt.Errorf("%w: id is required", ErrInvalidArgument)
	}
	m, err := scanManga(r.db.QueryRowContext(ctx, selectManga+` WHERE m.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Manga{}, fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	if err != nil {
		return Manga{}, fmt.Errorf("get manga %q: %w", id, err)
	}
	return m, nil
}

// UpdateChapters raises total_chapters (a new chapter release) and returns
// the updated record.
// Errors: ErrInvalidArgument (empty id, or total <= current value),
// ErrNotFound (unknown id).
//
// The comparison happens inside one conditional UPDATE, so two concurrent
// releases can never move the count backwards: SQLite executes the statement
// atomically, and the second release only succeeds if it is still larger.
func (r *Repo) UpdateChapters(ctx context.Context, id string, total int) (Manga, error) {
	if strings.TrimSpace(id) == "" {
		return Manga{}, fmt.Errorf("%w: id is required", ErrInvalidArgument)
	}
	res, err := r.db.ExecContext(ctx,
		`UPDATE manga SET total_chapters = ? WHERE id = ? AND total_chapters < ?`, total, id, total)
	if err != nil {
		return Manga{}, fmt.Errorf("update chapters %q: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return Manga{}, fmt.Errorf("update chapters %q: %w", id, err)
	}
	if n == 0 {
		// Nothing changed: either the manga does not exist, or the new value
		// is not larger. Get tells the two apart (and returns ErrNotFound).
		cur, err := r.Get(ctx, id)
		if err != nil {
			return Manga{}, err
		}
		return Manga{}, fmt.Errorf("%w: total_chapters must be greater than current value %d (got %d)",
			ErrInvalidArgument, cur.TotalChapters, total)
	}
	return r.Get(ctx, id)
}

// rowScanner is satisfied by *sql.Row and *sql.Rows.
type rowScanner interface{ Scan(dest ...any) error }

func scanManga(s rowScanner) (Manga, error) {
	var (
		m      Manga
		genres string
	)
	if err := s.Scan(&m.ID, &m.Title, &m.Author, &m.Status, &m.TotalChapters, &m.Description, &genres); err != nil {
		return Manga{}, err
	}
	m.Genres = []string{}
	if genres != "" {
		m.Genres = strings.Split(genres, ",")
	}
	return m, nil
}

// escapeLike escapes the LIKE wildcards % and _ (and the escape char itself).
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}
