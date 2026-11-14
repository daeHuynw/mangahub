package api

import (
	"testing"
)

func TestMangaSearchAndDetail(t *testing.T) {
	e := newEnv(t)
	tok := e.registerLogin("alice")

	code, body, _ := e.do("GET", "/api/manga?q=piece", nil, bearer(tok))
	if items := body["items"].([]any); code != 200 || len(items) != 1 {
		t.Fatalf("search: %d %v", code, body)
	}
	code, body, _ = e.do("GET", "/api/manga?q=zzzz", nil, bearer(tok))
	if items, ok := body["items"].([]any); code != 200 || !ok || len(items) != 0 {
		t.Fatalf("empty search must return items: [] — got %d %v", code, body)
	}
	code, body, _ = e.do("GET", "/api/manga/one-piece", nil, bearer(tok))
	if code != 200 || body["title"] != "One Piece" || body["total_chapters"].(float64) != 1130 {
		t.Fatalf("detail: %d %v", code, body)
	}
	code, body, _ = e.do("GET", "/api/manga/does-not-exist", nil, bearer(tok))
	expect(t, "unknown manga", code, 404, body)
}

func TestCatalogDownGives503(t *testing.T) {
	e := newEnv(t)
	tok := e.registerLogin("alice")
	e.cat.setDown(true)
	for _, p := range []string{"/api/manga?q=one", "/api/manga/one-piece"} {
		code, body, _ := e.do("GET", p, nil, bearer(tok))
		expect(t, p, code, 503, body)
	}
}
