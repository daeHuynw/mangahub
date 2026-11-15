package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Library statuses allowed by the CHECK constraint.
const (
	StatusPlanToRead = "plan-to-read"
	StatusReading    = "reading"
	StatusCompleted  = "completed"
)

func ValidStatus(s string) bool {
	return s == StatusPlanToRead || s == StatusReading || s == StatusCompleted
}

type LibraryEntry struct {
	UserID         int64
	MangaID        string
	Status         string
	CurrentChapter int
	UpdatedAt      time.Time
	Version        int64
}

// AddToLibrary inserts (userID, mangaID). A duplicate returns ErrConflict.
func (s *Store) AddToLibrary(ctx context.Context, userID int64, mangaID, status string) (LibraryEntry, error) {
	if status == "" {
		status = StatusPlanToRead
	}
	if mangaID == "" || !ValidStatus(status) {
		return LibraryEntry{}, ErrInvalidInput
	}
	ts := now()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO user_library (user_id, manga_id, status, current_chapter, updated_at, version)
		 VALUES (?, ?, ?, 0, ?, 1)`, userID, mangaID, status, ts)
	if isUnique(err) {
		return LibraryEntry{}, ErrConflict
	}
	if err != nil {
		return LibraryEntry{}, wrap("add to library", err)
	}
	return LibraryEntry{UserID: userID, MangaID: mangaID, Status: status, UpdatedAt: parseTime(ts), Version: 1}, nil
}

// ListLibrary returns one user's entries, most recently updated first.
func (s *Store) ListLibrary(ctx context.Context, userID int64) ([]LibraryEntry, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT user_id, manga_id, status, current_chapter, updated_at, version
		 FROM user_library WHERE user_id = ?
		 ORDER BY updated_at DESC, rowid DESC`, userID)
	if err != nil {
		return nil, wrap("list library", err)
	}
	defer rows.Close()
	out := []LibraryEntry{}
	for rows.Next() {
		var e LibraryEntry
		var ts string
		if err := rows.Scan(&e.UserID, &e.MangaID, &e.Status, &e.CurrentChapter, &ts, &e.Version); err != nil {
			return nil, wrap("scan library", err)
		}
		e.UpdatedAt = parseTime(ts)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, wrap("list library", err)
	}
	return out, nil
}

func (s *Store) GetLibraryEntry(ctx context.Context, userID int64, mangaID string) (LibraryEntry, error) {
	var e LibraryEntry
	var ts string
	err := s.db.QueryRowContext(ctx,
		`SELECT user_id, manga_id, status, current_chapter, updated_at, version
		 FROM user_library WHERE user_id = ? AND manga_id = ?`, userID, mangaID).
		Scan(&e.UserID, &e.MangaID, &e.Status, &e.CurrentChapter, &ts, &e.Version)
	if errors.Is(err, sql.ErrNoRows) {
		return LibraryEntry{}, ErrNotFound
	}
	if err != nil {
		return LibraryEntry{}, wrap("get library entry", err)
	}
	e.UpdatedAt = parseTime(ts)
	return e, nil
}

// UpdateProgress sets current_chapter (and optionally status) inside a
// transaction and bumps version by exactly one. The new row is returned so the
// caller can publish the TCP event with the committed version.
// status == "" keeps the existing status.
func (s *Store) UpdateProgress(ctx context.Context, userID int64, mangaID string, chapter int, status string) (LibraryEntry, error) {
	if chapter < 0 || (status != "" && !ValidStatus(status)) {
		return LibraryEntry{}, ErrInvalidInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return LibraryEntry{}, wrap("begin", err)
	}
	defer tx.Rollback() // no-op after Commit

	var e LibraryEntry
	var ts string
	err = tx.QueryRowContext(ctx,
		`UPDATE user_library
		 SET current_chapter = ?,
		     status = CASE WHEN ? = '' THEN status ELSE ? END,
		     updated_at = ?,
		     version = version + 1
		 WHERE user_id = ? AND manga_id = ?
		 RETURNING user_id, manga_id, status, current_chapter, updated_at, version`,
		chapter, status, status, now(), userID, mangaID).
		Scan(&e.UserID, &e.MangaID, &e.Status, &e.CurrentChapter, &ts, &e.Version)
	if errors.Is(err, sql.ErrNoRows) {
		return LibraryEntry{}, ErrNotFound
	}
	if err != nil {
		return LibraryEntry{}, wrap("update progress", err)
	}
	if err := tx.Commit(); err != nil {
		return LibraryEntry{}, wrap("commit", err)
	}
	e.UpdatedAt = parseTime(ts)
	return e, nil
}

// UpdateStatus changes only the status (also bumps version).
func (s *Store) UpdateStatus(ctx context.Context, userID int64, mangaID, status string) (LibraryEntry, error) {
	if !ValidStatus(status) {
		return LibraryEntry{}, ErrInvalidInput
	}
	var e LibraryEntry
	var ts string
	err := s.db.QueryRowContext(ctx,
		`UPDATE user_library SET status = ?, updated_at = ?, version = version + 1
		 WHERE user_id = ? AND manga_id = ?
		 RETURNING user_id, manga_id, status, current_chapter, updated_at, version`,
		status, now(), userID, mangaID).
		Scan(&e.UserID, &e.MangaID, &e.Status, &e.CurrentChapter, &ts, &e.Version)
	if errors.Is(err, sql.ErrNoRows) {
		return LibraryEntry{}, ErrNotFound
	}
	if err != nil {
		return LibraryEntry{}, wrap("update status", err)
	}
	e.UpdatedAt = parseTime(ts)
	return e, nil
}
