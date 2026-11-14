package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func mustUser(t *testing.T, s *Store, name string) User {
	t.Helper()
	u, err := s.CreateUser(context.Background(), name, "hash-"+name)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestCreateUserDuplicate(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	mustUser(t, s, "alice")
	if _, err := s.CreateUser(ctx, "alice", "x"); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate user: got %v, want ErrConflict", err)
	}
}

func TestUserByNameNotFound(t *testing.T) {
	s := newStore(t)
	if _, err := s.UserByName(context.Background(), "ghost"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}

func TestUserPersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "persist.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUser(context.Background(), "alice", "h"); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	u, err := s2.UserByName(context.Background(), "alice")
	if err != nil || u.PasswordHash != "h" {
		t.Fatalf("after reopen: %+v, %v", u, err)
	}
}
