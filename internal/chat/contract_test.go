package chat

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"mangahub/internal/api"
	"mangahub/internal/auth"
	"mangahub/internal/store"
)

// login registers (if needed) and logs in through the backend's HTTP API.
func login(t *testing.T, base, user string) string {
	t.Helper()
	cred, _ := json.Marshal(map[string]string{"username": user, "password": "secret123"})
	http.Post(base+"/api/auth/register", "application/json", bytes.NewReader(cred))
	resp, err := http.Post(base+"/api/auth/login", "application/json", bytes.NewReader(cred))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Token string `json:"token"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	if out.Token == "" {
		t.Fatalf("login %s: HTTP %d", user, resp.StatusCode)
	}
	return out.Token
}

// scenario is the F05 demo: Alice and Bob chat in general, Charlie is in
// another room, Bob disconnects and the service keeps working.
func scenario(t *testing.T, base string, names [3]string) {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(base, "http") + "/ws"
	if _, resp, err := websocket.DefaultDialer.Dial(wsURL+"?token=forged", nil); err == nil || resp.StatusCode != 401 {
		t.Fatalf("forged token: err=%v, want 401", err)
	}
	peers := make([]*peer, 3)
	for i, n := range names {
		peers[i] = dial(t, wsURL, login(t, base, n))
		peers[i].name = n
		peers[i].expect("system", n+" joined")
	}
	alice, bob, charlie := peers[0], peers[1], peers[2]

	charlie.send(Message{Type: "join", Room: "one-piece"})
	charlie.expect("system", names[2]+" joined")

	alice.send(Message{Type: "chat", Room: "general", Text: "Chapter 1131 is out!"})
	if m := bob.expect("chat", "1131"); m.From != names[0] {
		t.Fatalf("bob got %+v", m)
	}
	charlie.send(Message{Type: "chat", Room: "one-piece", Text: "spoilers here"})
	if m := charlie.nextChat(); m.Text != "spoilers here" {
		t.Fatalf("charlie got %+v from general", m)
	}

	bob.conn.Close()
	alice.expect("system", names[1]+" left")
	alice.send(Message{Type: "chat", Text: "still working"})
	alice.expect("chat", "still working")
}

// Contract test: the chat handler mounted through member A's api.Deps.WS
// with a real JWT from A's login endpoint (A's code only imported).
func TestContract_ThroughBackendRouter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	st, err := store.Open(filepath.Join(t.TempDir(), "backend.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	jwt := auth.NewManager("test-secret", time.Hour)
	srv := New(JWTVerifier(jwt))
	defer srv.Close()
	ts := httptest.NewServer(api.NewRouter(api.Deps{Store: st, Auth: jwt, AdminKey: "k", WS: srv.Handler()}))
	defer ts.Close()

	scenario(t, ts.URL, [3]string{"alice", "bob", "charlie"})
}

// TestE2E_LiveBackend runs the same scenario against a running backend
// (MANGAHUB_E2E_HTTP=http://localhost:8080).
func TestE2E_LiveBackend(t *testing.T) {
	base := os.Getenv("MANGAHUB_E2E_HTTP")
	if base == "" {
		t.Skip("set MANGAHUB_E2E_HTTP to run against a live backend")
	}
	suffix := time.Now().Format("150405")
	scenario(t, base, [3]string{"wsa" + suffix, "wsb" + suffix, "wsc" + suffix})
}
