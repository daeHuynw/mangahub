package api

import (
	"fmt"
	"testing"
)

func TestLibraryFlow(t *testing.T) {
	e := newEnv(t)
	tok := e.registerLogin("alice")
	h := bearer(tok)

	code, body, _ := e.do("POST", "/api/library", map[string]string{"manga_id": "one-piece"}, h)
	expect(t, "add", code, 201, body)
	if body["status"] != "plan-to-read" || body["title"] != "One Piece" || body["version"].(float64) != 1 {
		t.Fatalf("add body: %v", body)
	}
	code, body, _ = e.do("POST", "/api/library", map[string]string{"manga_id": "one-piece"}, h)
	expect(t, "add duplicate", code, 409, body)
	code, body, _ = e.do("POST", "/api/library", map[string]string{"manga_id": "ghost"}, h)
	expect(t, "add unknown manga", code, 404, body)
	code, body, _ = e.do("POST", "/api/library", map[string]string{"manga_id": "naruto", "status": "dropped"}, h)
	expect(t, "add bad status", code, 400, body)

	code, body, _ = e.do("GET", "/api/library", nil, h)
	if items := body["items"].([]any); code != 200 || len(items) != 1 {
		t.Fatalf("list: %d %v", code, body)
	}
}

func TestProgressValidation(t *testing.T) {
	e := newEnv(t)
	tok := e.registerLogin("alice")
	h := bearer(tok)
	e.do("POST", "/api/library", map[string]string{"manga_id": "one-piece"}, h)

	cases := []struct {
		name string
		path string
		body any
		want int
	}{
		{"chapter > total", "/api/library/one-piece/progress", map[string]any{"current_chapter": 99999}, 400},
		{"negative", "/api/library/one-piece/progress", map[string]any{"current_chapter": -1}, 400},
		{"missing field", "/api/library/one-piece/progress", map[string]any{"status": "reading"}, 400},
		{"string chapter", "/api/library/one-piece/progress", `{"current_chapter":"ten"}`, 400},
		{"bad status", "/api/library/one-piece/progress", map[string]any{"current_chapter": 1, "status": "dropped"}, 400},
		{"unknown manga", "/api/library/ghost/progress", map[string]any{"current_chapter": 1}, 404},
		{"not in library", "/api/library/naruto/progress", map[string]any{"current_chapter": 1}, 404},
	}
	for _, c := range cases {
		code, body, _ := e.do("PUT", c.path, c.body, h)
		expect(t, c.name, code, c.want, body)
	}
	// Boundary values are allowed.
	for _, ch := range []int{0, 1130} {
		code, body, _ := e.do("PUT", "/api/library/one-piece/progress", map[string]any{"current_chapter": ch}, h)
		expect(t, fmt.Sprintf("boundary %d", ch), code, 200, body)
	}
}

// User B must not see or edit user A's library (identity comes from the JWT).
func TestLibraryIsolation(t *testing.T) {
	e := newEnv(t)
	alice := e.registerLogin("alice")
	bob := e.registerLogin("bob")
	e.do("POST", "/api/library", map[string]string{"manga_id": "one-piece"}, bearer(alice))

	code, body, _ := e.do("GET", "/api/library", nil, bearer(bob))
	if items := body["items"].([]any); code != 200 || len(items) != 0 {
		t.Fatalf("bob sees alice's library: %v", body)
	}
	// Even if bob sends a user_id in the body it is ignored.
	code, body, _ = e.do("PUT", "/api/library/one-piece/progress", map[string]any{"current_chapter": 5, "user_id": 1}, bearer(bob))
	expect(t, "bob edits alice", code, 404, body)
}

func TestLibraryRoutesNeedToken(t *testing.T) {
	e := newEnv(t)
	for _, r := range []struct{ m, p string }{
		{"GET", "/api/library"}, {"POST", "/api/library"}, {"PUT", "/api/library/one-piece/progress"},
	} {
		code, body, _ := e.do(r.m, r.p, nil, nil)
		expect(t, r.m+" "+r.p, code, 401, body)
	}
	code, body, _ := e.do("GET", "/api/library", nil, bearer("forged.token.value"))
	expect(t, "forged token", code, 401, body)
}

func TestLibraryPersistsAfterRestart(t *testing.T) {
	e := newEnv(t)
	tok := e.registerLogin("alice")
	e.do("POST", "/api/library", map[string]string{"manga_id": "one-piece"}, bearer(tok))
	e.restart()
	code, body, _ := e.do("POST", "/api/auth/login", map[string]string{"username": "alice", "password": "secret123"}, nil)
	expect(t, "login after restart", code, 200, body)
	code, body, _ = e.do("GET", "/api/library", nil, bearer(body["token"].(string)))
	if items := body["items"].([]any); code != 200 || len(items) != 1 {
		t.Fatalf("library after restart: %d %v", code, body)
	}
}

func TestAddWhileCatalogDown(t *testing.T) {
	e := newEnv(t)
	tok := e.registerLogin("alice")
	e.cat.setDown(true)
	code, body, _ := e.do("POST", "/api/library", map[string]string{"manga_id": "one-piece"}, bearer(tok))
	expect(t, "add while catalog down", code, 503, body)
}
