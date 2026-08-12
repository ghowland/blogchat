package main

import (
	"context"
	"database/sql"
	"net/http"
	"sync/atomic"
	"time"
)

// App holds all process state. One instance exists for the life of the
// program. Every field is safe for concurrent use.
type App struct {
	conf   atomic.Pointer[Config]
	geo    atomic.Pointer[Geo]
	dbh    *sql.DB
	views  *Views
	limits *Limiter
	secret []byte
	seen   *SeenCache
	start  time.Time
}

// User is one member. The email address never goes into a template.
type User struct {
	ID            int64
	Email         string
	Handle        string
	InvitedBy     sql.NullInt64
	InviterHandle string
	CreatedAt     int64
	Enabled       bool
}

// Session is one active login key.
type Session struct {
	ID        int64
	UserID    int64
	CreatedAt int64
	LastSeen  int64
	IP        string
	Agent     string
	Current   bool
}

// Post is one thread or one channel. The creator owns it. With IsChat true,
// the row is a chat channel, the subject is the channel name, and the body
// is the topic line.
type Post struct {
	ID        int64
	UserID    int64
	Handle    string
	Subject   string
	Body      string
	CreatedAt int64
	UpdatedAt int64
	IsChat    bool
	LastAt    int64
}

// Reply is one comment inside a thread.
type Reply struct {
	ID        int64
	PostID    int64
	UserID    int64
	Handle    string
	Body      string
	CreatedAt int64
}

// FeedRow is one line of the post list or of the channel list.
type FeedRow struct {
	ID        int64
	Subject   string
	Handle    string
	CreatedAt int64
	LastAt    int64
	Replies   int64
	IsChat    bool
}

// AuthHandler is a handler that runs only with a valid session.
// The last parameter is the raw session token, needed for the CSRF check.
type AuthHandler func(res http.ResponseWriter, req *http.Request, usr *User, raw string)

type contextKey string

// ctxCSRFKey holds the CSRF token for the current request.
// RequireAuth sets the value. Base reads the value.
const ctxCSRFKey = contextKey("csrf")

// Conf returns the current configuration. SIGHUP can replace it at any time.
func (app *App) Conf() *Config {
	return app.conf.Load()
}

// GeoTable returns the current country table and block list.
func (app *App) GeoTable() *Geo {
	return app.geo.Load()
}

// WithCSRF returns a request that carries the given CSRF token.
func WithCSRF(req *http.Request, csrf string) *http.Request {
	return req.WithContext(context.WithValue(req.Context(), ctxCSRFKey, csrf))
}

// CSRFFromContext reads the CSRF token that WithCSRF stored.
func CSRFFromContext(req *http.Request) string {
	val, valid := req.Context().Value(ctxCSRFKey).(string)
	if !valid {
		return ""
	}
	return val
}
