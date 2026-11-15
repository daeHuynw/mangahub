// Package store owns the backend SQLite database: users and user_library.
// The manga catalog is NOT here; it belongs to the gRPC Catalog Service.
package store

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

// Sentinel errors. Handlers map them to HTTP status codes with errors.Is.
var (
	ErrNotFound     = errors.New("not found")
	ErrConflict     = errors.New("already exists")
	ErrInvalidInput = errors.New("invalid input")
	ErrUnavailable  = errors.New("database unavailable")
)

type Store struct {
	db *sql.DB
}

type User struct {
	ID           int64
	Username     string
	PasswordHash string
	CreatedAt    time.Time
}

const schema = `
CREATE TABLE IF NOT EXISTS users (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	username      TEXT    NOT NULL UNIQUE,
	password_hash TEXT    NOT NULL,
	created_at    TEXT    NOT NULL
);
CREATE TABLE IF NOT EXISTS user_library (
	user_id         INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	manga_id        TEXT    NOT NULL,
	status          TEXT    NOT NULL DEFAULT 'plan-to-read'
	                CHECK (status IN ('plan-to-read','reading','completed')),
	current_chapter INTEGER NOT NULL DEFAULT 0 CHECK (current_chapter >= 0),
	updated_at      TEXT    NOT NULL,
	version         INTEGER NOT NULL DEFAULT 1,
	PRIMARY KEY (user_id, manga_id)
);
`

// Open opens (or creates) the SQLite file and applies the schema.
// path may be ":memory:" for tests.
func Open(path string) (*Store, error) {
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	if path != ":memory:" {
		q.Add("_pragma", "journal_mode(WAL)")
	}
	dsn := "file:" + path + "?" + q.Encode()

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// SQLite allows one writer at a time. A single connection serialises
	// writes inside Go instead of returning SQLITE_BUSY under load, and keeps
	// an in-memory database shared across calls.
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
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// isUnique reports whether err is a UNIQUE / PRIMARY KEY constraint violation.
func isUnique(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "PRIMARY KEY constraint failed")
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }

func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

// wrap converts unexpected driver errors into ErrUnavailable while keeping the
// original message for logs.
func wrap(op string, err error) error {
	return fmt.Errorf("%s: %w: %v", op, ErrUnavailable, err)
}

// ---------------------------------------------------------------- users

// CreateUser inserts a user. The caller must pass an already hashed password.
func (s *Store) CreateUser(ctx context.Context, username, passwordHash string) (User, error) {
	if username == "" || passwordHash == "" {
		return User{}, ErrInvalidInput
	}
	created := now()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO users (username, password_hash, created_at) VALUES (?, ?, ?)`,
		username, passwordHash, created)
	if isUnique(err) {
		return User{}, ErrConflict
	}
	if err != nil {
		return User{}, wrap("create user", err)
	}
	id, _ := res.LastInsertId()
	return User{ID: id, Username: username, PasswordHash: passwordHash, CreatedAt: parseTime(created)}, nil
}

func (s *Store) UserByName(ctx context.Context, username string) (User, error) {
	return s.userWhere(ctx, "username = ?", username)
}

func (s *Store) UserByID(ctx context.Context, id int64) (User, error) {
	return s.userWhere(ctx, "id = ?", id)
}

func (s *Store) userWhere(ctx context.Context, where string, arg any) (User, error) {
	var u User
	var created string
	err := s.db.QueryRowContext(ctx,
		`SELECT id, username, password_hash, created_at FROM users WHERE `+where, arg).
		Scan(&u.ID, &u.Username, &u.PasswordHash, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, wrap("get user", err)
	}
	u.CreatedAt = parseTime(created)
	return u, nil
}
