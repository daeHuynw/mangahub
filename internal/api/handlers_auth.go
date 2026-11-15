package api

import (
	"errors"
	"log"
	"net/http"
	"regexp"

	"github.com/gin-gonic/gin"

	"mangahub/internal/auth"
	"mangahub/internal/store"
)

var usernameRe = regexp.MustCompile(`^[A-Za-z0-9]{3,32}$`)

type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func validateCredentials(cr credentials) string {
	if !usernameRe.MatchString(cr.Username) {
		return "username must be 3-32 letters or digits"
	}
	// bcrypt ignores bytes after 72, so longer passwords are rejected.
	if len(cr.Password) < 6 || len(cr.Password) > 72 {
		return "password must be 6-72 characters"
	}
	return ""
}

// POST /api/auth/register → 201 {"id","username"} | 400 | 409
func (s *server) register(c *gin.Context) {
	var cr credentials
	if err := c.ShouldBindJSON(&cr); err != nil {
		fail(c, http.StatusBadRequest, "body must be JSON {\"username\",\"password\"}")
		return
	}
	if msg := validateCredentials(cr); msg != "" {
		fail(c, http.StatusBadRequest, msg)
		return
	}
	hash, err := auth.HashPassword(cr.Password)
	if err != nil {
		log.Printf("register: hash: %v", err)
		fail(c, http.StatusInternalServerError, "internal error")
		return
	}
	u, err := s.Store.CreateUser(c.Request.Context(), cr.Username, hash)
	switch {
	case errors.Is(err, store.ErrConflict):
		fail(c, http.StatusConflict, "username already taken")
		return
	case err != nil:
		log.Printf("register: %v", err)
		fail(c, http.StatusInternalServerError, "internal error")
		return
	}
	c.JSON(http.StatusCreated, gin.H{"id": u.ID, "username": u.Username})
}

// POST /api/auth/login → 200 {"token","user":{"id","username"}} | 400 | 401
func (s *server) login(c *gin.Context) {
	var cr credentials
	if err := c.ShouldBindJSON(&cr); err != nil || cr.Username == "" || cr.Password == "" {
		fail(c, http.StatusBadRequest, "username and password are required")
		return
	}
	u, err := s.Store.UserByName(c.Request.Context(), cr.Username)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		log.Printf("login: %v", err)
		fail(c, http.StatusInternalServerError, "internal error")
		return
	}
	// Same message for unknown user and wrong password: don't reveal which.
	if err != nil || !auth.CheckPassword(u.PasswordHash, cr.Password) {
		fail(c, http.StatusUnauthorized, "invalid username or password")
		return
	}
	tok, err := s.Auth.Sign(u.ID, u.Username)
	if err != nil {
		log.Printf("login: sign: %v", err)
		fail(c, http.StatusInternalServerError, "internal error")
		return
	}
	c.JSON(http.StatusOK, gin.H{"token": tok, "user": gin.H{"id": u.ID, "username": u.Username}})
}

// GET /api/me → 200 {"id","username"}
func (s *server) me(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"id": auth.UserID(c), "username": auth.Username(c)})
}
