package main

import (
	"embed"
	"io/fs"
	"log"
	"net/http"
)

//go:embed static
var staticFS embed.FS

// Routes builds the router. The patterns use the method form of Go 1.22,
// so no external router package is necessary.
//
// Every route except the four public routes needs a session, because a
// member must sign in to read a post and to write a post.
func (app *App) Routes() http.Handler {
	mux := http.NewServeMux()

	// Public routes.
	mux.HandleFunc("GET /", app.ShowLogin)
	mux.HandleFunc("POST /login", app.SubmitLogin)
	mux.HandleFunc("GET /l/{token}", app.ConsumeLink)
	mux.HandleFunc("GET /terms", app.ShowTerms)

	// Member routes.
	mux.HandleFunc("GET /feed", app.RequireAuth(app.ShowFeed))
	mux.HandleFunc("GET /p/{id}", app.RequireAuth(app.ShowPost))
	mux.HandleFunc("POST /p", app.RequireAuth(app.CreatePostHandler))
	mux.HandleFunc("POST /p/{id}/delete", app.RequireAuth(app.DeletePostHandler))
	mux.HandleFunc("POST /p/{id}/r", app.RequireAuth(app.CreateReplyHandler))
	mux.HandleFunc("POST /r/{id}/delete", app.RequireAuth(app.DeleteReplyHandler))
	// Chat channels. The path root /c/ keeps PathValue("id") unambiguous
	// and makes the kind visible in the address.
	mux.HandleFunc("GET /chat", app.RequireAuth(app.ShowChats))
	mux.HandleFunc("POST /chat", app.RequireAuth(app.CreateChannelHandler))
	mux.HandleFunc("GET /c/{id}", app.RequireAuth(app.ShowChannel))
	mux.HandleFunc("POST /c/{id}/m", app.RequireAuth(app.CreateChatLineHandler))
	mux.HandleFunc("POST /c/{id}/delete", app.RequireAuth(app.DeletePostHandler))
	mux.HandleFunc("GET /keys", app.RequireAuth(app.ShowKeys))
	mux.HandleFunc("POST /keys/revoke-others", app.RequireAuth(app.RevokeOtherKeys))
	mux.HandleFunc("POST /logout", app.RequireAuth(app.Logout))
	mux.HandleFunc("GET /invite", app.RequireAuth(app.ShowInvite))
	mux.HandleFunc("POST /invite", app.RequireAuth(app.SubmitInvite))

	// Static files. The style sheet is the only asset.
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		log.Fatalf("static files: %v", err)
	}
	mux.Handle("GET /static/", http.StripPrefix("/static/",
		cacheControl(http.FileServer(http.FS(sub)))))

	return app.logRequest(mux)
}

// cacheControl adds a long cache time to the static files.
func cacheControl(next http.Handler) http.Handler {
	return http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
		res.Header().Set("Cache-Control", "public, max-age=3600")
		next.ServeHTTP(res, req)
	})
}

// logRequest writes one line for each request. The line holds no email
// address and no token.
func (app *App) logRequest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
		path := req.URL.Path
		// A sign-in link holds a secret, so the log keeps the prefix only.
		if len(path) > 3 && path[:3] == "/l/" {
			path = "/l/[token]"
		}
		log.Printf("%s %s", req.Method, path)
		next.ServeHTTP(res, req)
	})
}
