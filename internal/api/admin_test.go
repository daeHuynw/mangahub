package api

import (
	"testing"
)

func TestAdminRelease(t *testing.T) {
	e := newEnv(t)
	path := "/api/admin/manga/one-piece/release"

	code, body, _ := e.do("POST", path, map[string]int{"total_chapters": 1131}, nil)
	expect(t, "no key", code, 403, body)
	code, body, _ = e.do("POST", path, map[string]int{"total_chapters": 1131}, map[string]string{"X-Admin-Key": "wrong"})
	expect(t, "wrong key", code, 403, body)

	key := map[string]string{"X-Admin-Key": adminKey}
	code, body, _ = e.do("POST", path, map[string]int{"total_chapters": 1131}, key)
	expect(t, "release", code, 200, body)
	if body["notified"].(float64) != 2 || body["total_chapters"].(float64) != 1131 || e.bcast.calls != 1 {
		t.Fatalf("release body %v, broadcasts %d", body, e.bcast.calls)
	}

	code, body, _ = e.do("POST", path, map[string]int{"total_chapters": 10}, key)
	expect(t, "decrease", code, 400, body)
	code, body, _ = e.do("POST", "/api/admin/manga/ghost/release", map[string]int{"total_chapters": 10}, key)
	expect(t, "unknown manga", code, 404, body)
	code, body, _ = e.do("POST", path, map[string]any{}, key)
	expect(t, "missing body", code, 400, body)

	e.cat.setDown(true)
	code, body, _ = e.do("POST", path, map[string]int{"total_chapters": 1200}, key)
	expect(t, "catalog down", code, 503, body)
	if e.bcast.calls != 1 {
		t.Fatalf("failed releases must not broadcast UDP (calls=%d)", e.bcast.calls)
	}
}
