package api

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"sync"
	"testing"
)

// 20 concurrent PUTs → exactly 20 version bumps (D6).
func TestConcurrentProgress(t *testing.T) {
	e := newEnv(t)
	tok := e.registerLogin("alice")
	e.do("POST", "/api/library", map[string]string{"manga_id": "one-piece"}, bearer(tok))

	var wg sync.WaitGroup
	errs := make(chan string, 20)
	for i := 1; i <= 20; i++ {
		wg.Add(1)
		go func(ch int) {
			defer wg.Done()
			body, _ := json.Marshal(map[string]int{"current_chapter": ch})
			req := httptest.NewRequest("PUT", "/api/library/one-piece/progress", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+tok)
			w := httptest.NewRecorder()
			e.router.ServeHTTP(w, req)
			if w.Code != 200 {
				errs <- w.Body.String()
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for msg := range errs {
		t.Error(msg)
	}
	_, body, _ := e.do("GET", "/api/library", nil, bearer(tok))
	item := body["items"].([]any)[0].(map[string]any)
	if item["version"].(float64) != 21 {
		t.Fatalf("version = %v, want 21", item["version"])
	}
}
