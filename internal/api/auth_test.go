package api

import (
	"fmt"
	"testing"
)

func TestHealth(t *testing.T) {
	e := newEnv(t)
	code, body, _ := e.do("GET", "/api/health", nil, nil)
	if code != 200 || body["status"] != "ok" {
		t.Fatalf("%d %v", code, body)
	}
}

func TestRegisterAndLogin(t *testing.T) {
	e := newEnv(t)
	cred := map[string]string{"username": "alice", "password": "secret123"}
	code, body, _ := e.do("POST", "/api/auth/register", cred, nil)
	expect(t, "register", code, 201, body)
	code, body, _ = e.do("POST", "/api/auth/register", cred, nil)
	expect(t, "duplicate register", code, 409, body)

	code, body, _ = e.do("POST", "/api/auth/login", map[string]string{"username": "alice", "password": "wrong-pw"}, nil)
	expect(t, "wrong password", code, 401, body)
	code, body, _ = e.do("POST", "/api/auth/login", map[string]string{"username": "nobody", "password": "secret123"}, nil)
	expect(t, "unknown user", code, 401, body)

	code, body, _ = e.do("POST", "/api/auth/login", cred, nil)
	expect(t, "login", code, 200, body)
	tok, _ := body["token"].(string)
	code, body, _ = e.do("GET", "/api/me", nil, bearer(tok))
	if code != 200 || body["username"] != "alice" {
		t.Fatalf("/api/me: %d %v", code, body)
	}
}

func TestRegisterValidation(t *testing.T) {
	e := newEnv(t)
	cases := []any{
		map[string]string{"username": "ab", "password": "secret123"},        // too short
		map[string]string{"username": "bad name!", "password": "secret123"}, // illegal chars
		map[string]string{"username": "alice", "password": "123"},           // short password
		"{not json", // malformed
	}
	for i, c := range cases {
		code, body, _ := e.do("POST", "/api/auth/register", c, nil)
		expect(t, fmt.Sprintf("case %d", i), code, 400, body)
	}
}

func TestProtectedRoutesNeedToken(t *testing.T) {
	e := newEnv(t)
	for _, r := range []struct{ m, p string }{
		{"GET", "/api/me"}, {"GET", "/api/manga?q=one"}, {"GET", "/api/manga/one-piece"},
	} {
		code, body, _ := e.do(r.m, r.p, nil, nil)
		expect(t, r.m+" "+r.p, code, 401, body)
	}
	code, body, _ := e.do("GET", "/api/me", nil, bearer("forged.token.value"))
	expect(t, "forged token", code, 401, body)
}

func TestUserPersistsAfterRestart(t *testing.T) {
	e := newEnv(t)
	e.registerLogin("alice")
	e.restart()
	code, body, _ := e.do("POST", "/api/auth/login", map[string]string{"username": "alice", "password": "secret123"}, nil)
	expect(t, "login after restart", code, 200, body)
}
