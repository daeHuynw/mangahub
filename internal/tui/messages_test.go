package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"mangahub/internal/tui/client"
)

func TestFriendlyError(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{fmt.Errorf("%w: dial tcp: connection refused", client.ErrUnreachable), "Cannot reach the backend at http://x"},
		{&client.APIError{Status: 400, Message: "current_chapter must be between 0 and 10"}, "Invalid request: current_chapter must be between 0 and 10"},
		{&client.APIError{Status: 401, Message: "invalid or expired token"}, "log in again"},
		{&client.APIError{Status: 404, Message: "manga not found"}, "Manga not found"},
		{&client.APIError{Status: 409, Message: "username already taken"}, "already taken"},
		{&client.APIError{Status: 503, Message: "catalog service unavailable"}, "catalog service is unavailable"},
		{&client.APIError{Status: 500, Message: "internal error"}, "HTTP 500"},
		{errors.New("boom"), "Unexpected error: boom"},
	}
	for _, tc := range cases {
		if got := friendlyError(tc.err, "http://x"); !strings.Contains(got, tc.want) {
			t.Errorf("friendlyError(%v) = %q, want it to contain %q", tc.err, got, tc.want)
		}
	}
	if friendlyError(nil, "x") != "" {
		t.Error("nil error should give empty text")
	}
}

func TestValidateCredentials(t *testing.T) {
	cases := map[[2]string]bool{ // {user, pass} → valid?
		{"alice", "secret123"}:                true,
		{"", "secret123"}:                     false,
		{"alice", ""}:                         false,
		{"al", "secret123"}:                   false,
		{"alice!", "secret123"}:               false,
		{strings.Repeat("a", 33), "secret12"}: false,
		{"alice", "12345"}:                    false,
		{"alice", strings.Repeat("p", 73)}:    false,
		{"Bob42", "123456"}:                   true,
	}
	for in, valid := range cases {
		if got := validateCredentials(in[0], in[1]) == ""; got != valid {
			t.Errorf("validateCredentials(%q, %q) valid=%v, want %v", in[0], in[1], got, valid)
		}
	}
}

var sample = []client.Manga{
	{ID: "a", Title: "A", Genres: []string{"action", "comedy"}},
	{ID: "b", Title: "B", Genres: []string{"sports"}},
	{ID: "c", Title: "C", Genres: []string{"comedy"}},
}

func TestGenresAndFilter(t *testing.T) {
	if got := strings.Join(genresOf(sample), ","); got != "action,comedy,sports" {
		t.Fatalf("genresOf = %s", got)
	}
	if got := filterByGenre(sample, "comedy"); len(got) != 2 || got[0].ID != "a" || got[1].ID != "c" {
		t.Fatalf("filter comedy = %+v", got)
	}
	if got := filterByGenre(sample, allGenres); len(got) != 3 {
		t.Fatalf("all genres = %d", len(got))
	}
	if got := filterByGenre(sample, "horror"); got == nil || len(got) != 0 {
		t.Fatalf("no match = %#v", got)
	}
}

func TestDetailsTextShowsAllFieldsAndEscapesTags(t *testing.T) {
	m := client.Manga{ID: "x", Title: "Evil [red]Title", Author: "Auth", Genres: []string{"action", "drama"},
		Status: "ongoing", TotalChapters: 42, Description: "Desc"}
	txt := detailsText(m, "HIT")
	for _, want := range []string{"Evil [red[]Title", "Auth", "action, drama", "ongoing", "42", "Desc", "HIT"} {
		if !strings.Contains(txt, want) {
			t.Errorf("details text missing %q:\n%s", want, txt)
		}
	}
	if strings.Contains(detailsText(m, ""), "X-Cache") {
		t.Error("X-Cache line shown although the backend sent none")
	}
	if !strings.Contains(listLabel(m), "Evil [red[]Title") {
		t.Error("list label not escaped")
	}
}

func TestLoadConfig(t *testing.T) {
	t.Setenv("MANGAHUB_HTTP", "")
	t.Setenv("MANGAHUB_TCP", "")
	t.Setenv("MANGAHUB_UDP", "")
	if c := LoadConfig(); c.HTTP != "http://localhost:8080" || c.TCP != "localhost:9090" || c.UDP != "localhost:9091" {
		t.Fatalf("defaults = %+v", c)
	}
	t.Setenv("MANGAHUB_HTTP", "http://10.0.0.5:8080")
	if c := LoadConfig(); c.HTTP != "http://10.0.0.5:8080" {
		t.Fatalf("override = %+v", c)
	}
}
