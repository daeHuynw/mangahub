// Package client is the TUI's HTTP client for the Gin backend (contracts.md §3).
//
// It knows nothing about tview: every method is a plain, blocking call that
// takes a context and returns typed data or an error. The UI layer runs these
// calls in goroutines and hands the results to the tview event loop. Keeping
// the network code here makes it testable with net/http/httptest.
//
// The TUI never talks to the gRPC Catalog Service: manga data always goes
// TUI → HTTP → Gin → gRPC → Catalog.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultTimeout bounds every HTTP request. The backend itself gives up on the
// catalog after 2 s, so 10 s is only reached if the backend is frozen.
const DefaultTimeout = 10 * time.Second

// SearchLimit is the page size asked for in SearchManga. It equals the
// catalog's maximum (internal/catalog.MaxLimit); larger values are capped there.
const SearchLimit = 200

// maxBody caps how much of a response is read, so a broken server cannot make
// the TUI allocate unbounded memory.
const maxBody = 1 << 20

// ErrUnreachable means no HTTP response was received at all (backend not
// running, wrong address, timeout). It is different from an APIError, where
// the backend answered with an error status.
var ErrUnreachable = errors.New("backend unreachable")

// APIError is a non-2xx response. Message is the backend's {"error": ...} text.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return fmt.Sprintf("HTTP %d: %s", e.Status, e.Message) }

// StatusOf returns the HTTP status carried by err, or 0 if err is not an APIError.
func StatusOf(err error) int {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Status
	}
	return 0
}

// User is the account returned by register/login.
type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

// Manga matches the contract's Manga object.
type Manga struct {
	ID            string   `json:"id"`
	Title         string   `json:"title"`
	Author        string   `json:"author"`
	Genres        []string `json:"genres"`
	Status        string   `json:"status"`
	TotalChapters int      `json:"total_chapters"`
	Description   string   `json:"description"`
}

// Client is safe for concurrent use: the token and session id are guarded
// because the TCP goroutine and UI actions may use it at the same time.
type Client struct {
	base string
	http *http.Client

	mu        sync.RWMutex
	token     string
	user      User
	sessionID string
}

// New returns a client for the backend at baseURL (e.g. "http://localhost:8080").
func New(baseURL string) *Client {
	return &Client{
		base: strings.TrimRight(baseURL, "/"),
		http: &http.Client{Timeout: DefaultTimeout},
	}
}

// BaseURL returns the backend address, for display in the UI.
func (c *Client) BaseURL() string { return c.base }

// Token returns the current JWT ("" when logged out). The TCP, UDP and
// WebSocket clients authenticate with it.
func (c *Client) Token() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.token
}

// User returns the logged-in user (zero value when logged out).
func (c *Client) User() User {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.user
}

// SetSessionID stores the TCP session id; it is sent as X-Session-ID so
// the backend does not echo our own progress updates back to us.
func (c *Client) SetSessionID(id string) {
	c.mu.Lock()
	c.sessionID = id
	c.mu.Unlock()
}

// Logout forgets the token locally (JWTs are stateless; nothing to call).
func (c *Client) Logout() {
	c.mu.Lock()
	c.token, c.user, c.sessionID = "", User{}, ""
	c.mu.Unlock()
}

// Health calls GET /api/health.
func (c *Client) Health(ctx context.Context) error {
	return c.do(ctx, http.MethodGet, "/api/health", nil, nil, nil)
}

// Register creates an account (201). Errors: 400 invalid input, 409 taken.
func (c *Client) Register(ctx context.Context, username, password string) (User, error) {
	var u User
	err := c.do(ctx, http.MethodPost, "/api/auth/register", credentials(username, password), &u, nil)
	return u, err
}

// Login authenticates and stores the JWT for later calls. Errors: 400, 401.
func (c *Client) Login(ctx context.Context, username, password string) (User, error) {
	var resp struct {
		Token string `json:"token"`
		User  User   `json:"user"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/auth/login", credentials(username, password), &resp, nil); err != nil {
		return User{}, err
	}
	if resp.Token == "" {
		return User{}, fmt.Errorf("login response has no token")
	}
	c.mu.Lock()
	c.token, c.user = resp.Token, resp.User
	c.mu.Unlock()
	return resp.User, nil
}

// SearchManga calls GET /api/manga?q=. An empty query lists the catalog.
// No match returns an empty slice. Errors: 401, 503.
func (c *Client) SearchManga(ctx context.Context, query string) ([]Manga, error) {
	var resp struct {
		Items []Manga `json:"items"`
	}
	// Without limit the backend returns at most 50 rows, which would silently
	// cut the list (and the genre options) once the catalog grows past that.
	path := "/api/manga?" + url.Values{
		"q":     {strings.TrimSpace(query)},
		"limit": {strconv.Itoa(SearchLimit)},
	}.Encode()
	if err := c.do(ctx, http.MethodGet, path, nil, &resp, nil); err != nil {
		return nil, err
	}
	if resp.Items == nil {
		resp.Items = []Manga{}
	}
	return resp.Items, nil
}

// GetManga calls GET /api/manga/:id. cache is the X-Cache header ("HIT",
// "MISS" or "" when Redis is off). Errors: 400, 401, 404, 503.
func (c *Client) GetManga(ctx context.Context, id string) (m Manga, cache string, err error) {
	if strings.TrimSpace(id) == "" {
		return Manga{}, "", &APIError{Status: http.StatusBadRequest, Message: "manga id is required"}
	}
	var hdr http.Header
	err = c.do(ctx, http.MethodGet, "/api/manga/"+url.PathEscape(id), nil, &m, &hdr)
	if hdr != nil {
		cache = hdr.Get("X-Cache")
	}
	return m, cache, err
}

func credentials(username, password string) map[string]string {
	return map[string]string{"username": username, "password": password}
}

// do sends one JSON request. body (if non-nil) is encoded as JSON, a 2xx
// response is decoded into out (if non-nil), and response headers are copied
// to *hdr (if non-nil). Non-2xx becomes *APIError; transport failures wrap
// ErrUnreachable.
func (c *Client) do(ctx context.Context, method, path string, body, out any, hdr *http.Header) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c.mu.RLock()
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if c.sessionID != "" {
		req.Header.Set("X-Session-ID", c.sessionID)
	}
	c.mu.RUnlock()

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	defer resp.Body.Close()
	if hdr != nil {
		*hdr = resp.Header
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return fmt.Errorf("%w: reading response: %v", ErrUnreachable, err)
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &APIError{Status: resp.StatusCode, Message: errorMessage(resp.StatusCode, data)}
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("decode %s %s response: %w", method, path, err)
		}
	}
	return nil
}

// errorMessage extracts {"error": "..."} or falls back to the status text
// (e.g. a proxy returning HTML).
func errorMessage(status int, data []byte) string {
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(data, &e) == nil && e.Error != "" {
		return e.Error
	}
	return http.StatusText(status)
}
