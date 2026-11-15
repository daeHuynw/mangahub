package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestPasswordHash(t *testing.T) {
	h, err := HashPassword("secret123")
	if err != nil {
		t.Fatal(err)
	}
	if h == "secret123" {
		t.Fatal("password stored in plain text")
	}
	if !CheckPassword(h, "secret123") || CheckPassword(h, "wrong") {
		t.Fatal("CheckPassword mismatch")
	}
}

func TestTokenValid(t *testing.T) {
	m := NewManager("s3cret", time.Hour)
	tok, err := m.Sign(7, "alice")
	if err != nil {
		t.Fatal(err)
	}
	c, err := m.Verify(tok)
	if err != nil || c.UID != 7 || c.Username != "alice" {
		t.Fatalf("verify: %+v %v", c, err)
	}
}

func TestTokenWrongSignature(t *testing.T) {
	tok, _ := NewManager("secret-A", time.Hour).Sign(1, "alice")
	if _, err := NewManager("secret-B", time.Hour).Verify(tok); err == nil {
		t.Fatal("token signed with another secret was accepted")
	}
	if _, err := NewManager("secret-A", time.Hour).Verify(tok + "x"); err == nil {
		t.Fatal("tampered token was accepted")
	}
}

func TestTokenExpired(t *testing.T) {
	m := NewManager("s", time.Hour)
	past := time.Now().Add(-2 * time.Hour)
	m.now = func() time.Time { return past }
	tok, _ := m.Sign(1, "alice")
	m.now = time.Now
	if _, err := m.Verify(tok); err == nil {
		t.Fatal("expired token was accepted")
	}
}

func TestMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	m := NewManager("s", time.Hour)
	r := gin.New()
	r.GET("/me", m.RequireAuth(), func(c *gin.Context) {
		c.JSON(200, gin.H{"uid": UserID(c), "username": Username(c)})
	})
	r.POST("/admin", RequireAdmin("k"), func(c *gin.Context) { c.Status(200) })

	do := func(method, path string, hdr map[string]string) int {
		req := httptest.NewRequest(method, path, nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}
	tok, _ := m.Sign(3, "bob")
	if got := do("GET", "/me", nil); got != http.StatusUnauthorized {
		t.Errorf("no token: %d", got)
	}
	if got := do("GET", "/me", map[string]string{"Authorization": "Bearer junk"}); got != http.StatusUnauthorized {
		t.Errorf("bad token: %d", got)
	}
	if got := do("GET", "/me", map[string]string{"Authorization": "Bearer " + tok}); got != http.StatusOK {
		t.Errorf("good token: %d", got)
	}
	if got := do("POST", "/admin", nil); got != http.StatusForbidden {
		t.Errorf("no admin key: %d", got)
	}
	if got := do("POST", "/admin", map[string]string{"X-Admin-Key": "k"}); got != http.StatusOK {
		t.Errorf("admin key: %d", got)
	}
}
