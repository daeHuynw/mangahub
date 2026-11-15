package api

import (
	"testing"
)

func TestProgressUpdateAndTCPPush(t *testing.T) {
	e := newEnv(t)
	tok := e.registerLogin("alice")
	h := bearer(tok)
	e.do("POST", "/api/library", map[string]string{"manga_id": "one-piece"}, h)

	h["X-Session-ID"] = "s7"
	code, body, _ := e.do("PUT", "/api/library/one-piece/progress", map[string]any{"current_chapter": 25, "status": "reading"}, h)
	expect(t, "progress", code, 200, body)
	if body["current_chapter"].(float64) != 25 || body["status"] != "reading" || body["version"].(float64) != 2 {
		t.Fatalf("progress body: %v", body)
	}
	if e.push.calls != 1 || e.push.except != "s7" || e.push.ev.Type != "progress_updated" ||
		*e.push.ev.CurrentChapter != 25 || e.push.ev.Version != 2 {
		t.Fatalf("tcp push: %+v", e.push)
	}

	code, body, _ = e.do("PUT", "/api/library/one-piece/progress", map[string]any{"current_chapter": 26}, h)
	if code != 200 || body["version"].(float64) != 3 || body["status"] != "reading" {
		t.Fatalf("second update: %d %v", code, body)
	}
}

func TestRejectedProgressDoesNotPush(t *testing.T) {
	e := newEnv(t)
	h := bearer(e.registerLogin("alice"))
	e.do("POST", "/api/library", map[string]string{"manga_id": "one-piece"}, h)
	for _, body := range []map[string]any{{"current_chapter": 99999}, {"current_chapter": -1}, {"status": "reading"}} {
		e.do("PUT", "/api/library/one-piece/progress", body, h)
	}
	e.do("PUT", "/api/library/naruto/progress", map[string]any{"current_chapter": 1}, h) // not in library
	if e.push.calls != 0 {
		t.Fatalf("rejected updates must not push TCP events (calls=%d)", e.push.calls)
	}
}
