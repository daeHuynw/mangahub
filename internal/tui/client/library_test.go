package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLibraryCalls_RequestShape(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/library":
			w.Write([]byte(`{"items":null}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/library":
			writeJSON(w, 201, LibraryItem{MangaID: "one-piece", Title: "One Piece", Status: "plan-to-read", TotalChapters: 1130, Version: 1})
		case r.Method == http.MethodPut && r.URL.EscapedPath() == "/api/library/one-piece/progress":
			writeJSON(w, 200, Progress{MangaID: "one-piece", CurrentChapter: 25, Status: "reading", Version: 2})
		default:
			writeJSON(w, 404, map[string]string{"error": "route not found"})
		}
	})
	c := New(f.srv.URL)
	ctx := context.Background()

	items, err := c.ListLibrary(ctx)
	if err != nil || items == nil || len(items) != 0 {
		t.Fatalf("ListLibrary = %#v, %v; want empty non-nil", items, err)
	}

	it, err := c.AddToLibrary(ctx, "one-piece", "")
	if err != nil || it.Version != 1 || it.TotalChapters != 1130 {
		t.Fatalf("AddToLibrary = %+v, %v", it, err)
	}
	if _, body := f.last(); body["manga_id"] != "one-piece" || body["status"] != "" {
		t.Fatalf("add body = %v; empty status must be omitted so the backend default applies", body)
	}

	c.SetSessionID("s2")
	p, err := c.UpdateProgress(ctx, "one-piece", 25, "reading")
	if err != nil || p.Version != 2 || p.CurrentChapter != 25 {
		t.Fatalf("UpdateProgress = %+v, %v", p, err)
	}
	req, _ := f.last()
	if req.Header.Get("X-Session-ID") != "s2" {
		t.Fatalf("X-Session-ID = %q", req.Header.Get("X-Session-ID"))
	}
}

// current_chapter must be sent as a JSON number, also when it is 0 (a
// pointer/omitempty bug would drop it and the backend would answer 400).
func TestUpdateProgress_SendsZeroChapterAsNumber(t *testing.T) {
	bodies := make(chan map[string]json.RawMessage, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var raw map[string]json.RawMessage
		json.NewDecoder(r.Body).Decode(&raw)
		bodies <- raw
		writeJSON(w, 200, Progress{Version: 2})
	}))
	defer srv.Close()
	if _, err := New(srv.URL).UpdateProgress(context.Background(), "x", 0, ""); err != nil {
		t.Fatal(err)
	}
	raw := <-bodies
	if string(raw["current_chapter"]) != "0" {
		t.Fatalf("current_chapter = %s, want 0", raw["current_chapter"])
	}
	if _, has := raw["status"]; has {
		t.Fatal("empty status should be omitted")
	}
}

func TestLibraryErrors(t *testing.T) {
	cases := []struct {
		status int
		msg    string
	}{
		{400, "current_chapter must be between 0 and 1130"},
		{404, "manga is not in your library"},
		{409, "manga already in your library"},
	}
	for _, tc := range cases {
		f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, tc.status, map[string]string{"error": tc.msg})
		})
		_, err := New(f.srv.URL).UpdateProgress(context.Background(), "one-piece", 99999, "")
		if ae, ok := err.(*APIError); !ok || ae.Status != tc.status || ae.Message != tc.msg {
			t.Errorf("%d: err = %#v", tc.status, err)
		}
	}
}
