// Package auth provides password hashing, JWT signing/verification and Gin
// middleware. The same Verify is reused by the TCP, UDP and WebSocket servers,
// so every protocol trusts exactly the same identity.
package auth

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

// Context keys set by RequireAuth.
const (
	CtxUserID   = "uid"
	CtxUsername = "username"
)

const DefaultTTL = 24 * time.Hour

var ErrInvalidToken = errors.New("invalid token")

// ------------------------------------------------------------ passwords

func HashPassword(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	return string(b), err
}

// CheckPassword returns true when pw matches the stored bcrypt hash.
func CheckPassword(hash, pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

// ------------------------------------------------------------ JWT

type Claims struct {
	UID      int64  `json:"uid"`
	Username string `json:"username"`
	jwt.RegisteredClaims
}

type Manager struct {
	secret []byte
	ttl    time.Duration
	now    func() time.Time // replaceable in tests
}

func NewManager(secret string, ttl time.Duration) *Manager {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &Manager{secret: []byte(secret), ttl: ttl, now: time.Now}
}

// Sign issues an HS256 token for the user.
func (m *Manager) Sign(uid int64, username string) (string, error) {
	now := m.now()
	claims := Claims{
		UID:      uid,
		Username: username,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   username,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(m.ttl)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(m.secret)
}

// Verify checks signature, algorithm and expiry, and returns the claims.
func (m *Manager) Verify(token string) (*Claims, error) {
	claims := &Claims{}
	parsed, err := jwt.ParseWithClaims(token, claims,
		func(t *jwt.Token) (any, error) { return m.secret, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithTimeFunc(m.now),
	)
	if err != nil || !parsed.Valid || claims.UID <= 0 {
		return nil, ErrInvalidToken
	}
	return claims, nil
}

// ------------------------------------------------------------ Gin middleware

// TokenFromRequest reads "Authorization: Bearer <jwt>", falling back to the
// ?token= query parameter (needed for WebSocket upgrades from some clients).
func TokenFromRequest(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	}
	return r.URL.Query().Get("token")
}

// RequireAuth rejects requests without a valid JWT (401) and stores the
// verified user id and name in the Gin context. Handlers must take the user id
// from here, never from the request body.
func (m *Manager) RequireAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		tok := TokenFromRequest(c.Request)
		if tok == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing token"})
			return
		}
		claims, err := m.Verify(tok)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired token"})
			return
		}
		c.Set(CtxUserID, claims.UID)
		c.Set(CtxUsername, claims.Username)
		c.Next()
	}
}

// RequireAdmin checks the X-Admin-Key header in constant time (403 otherwise).
func RequireAdmin(adminKey string) gin.HandlerFunc {
	return func(c *gin.Context) {
		got := c.GetHeader("X-Admin-Key")
		if adminKey == "" || subtle.ConstantTimeCompare([]byte(got), []byte(adminKey)) != 1 {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "admin key required"})
			return
		}
		c.Next()
	}
}

// UserID returns the verified user id stored by RequireAuth.
func UserID(c *gin.Context) int64 { return c.GetInt64(CtxUserID) }

// Username returns the verified username stored by RequireAuth.
func Username(c *gin.Context) string { return c.GetString(CtxUsername) }
