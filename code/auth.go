package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"
)

const (
	// sessionCookie is the name of the cookie that holds the session token.
	sessionCookie = "sid"

	// loginTokenLife is the life of a sign-in link.
	loginTokenLife = 15 * time.Minute

	// inviteTokenLife is the life of the first link of a new member.
	inviteTokenLife = 7 * 24 * time.Hour

	// loginRateLimit and loginRateWindow control the mail send rate.
	loginRateLimit  = 5
	loginRateWindow = 15 * time.Minute
)

// SetSessionCookie writes the session cookie. The Secure attribute is set
// when the site address uses HTTPS, so that a local HTTP test still works.
func (app *App) SetSessionCookie(res http.ResponseWriter, raw string) {
	conf := app.Conf()
	http.SetCookie(res, &http.Cookie{
		Name:     sessionCookie,
		Value:    raw,
		Path:     "/",
		MaxAge:   conf.SessionDays * 24 * 3600,
		HttpOnly: true,
		Secure:   strings.HasPrefix(conf.SiteURL, "https://"),
		SameSite: http.SameSiteLaxMode,
	})
}

// ClearSessionCookie removes the session cookie from the client.
func (app *App) ClearSessionCookie(res http.ResponseWriter) {
	conf := app.Conf()
	http.SetCookie(res, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   strings.HasPrefix(conf.SiteURL, "https://"),
		SameSite: http.SameSiteLaxMode,
	})
}

// SessionFrom reads the cookie and returns the member, the session row
// identifier, and the raw token. The last result is false when there is no
// valid session.
func (app *App) SessionFrom(req *http.Request) (*User, int64, string, bool) {
	cookie, err := req.Cookie(sessionCookie)
	if err != nil || cookie.Value == "" {
		return nil, 0, "", false
	}
	sum, valid := HashToken(cookie.Value)
	if !valid {
		return nil, 0, "", false
	}
	usr, sid, err := app.SessionByHash(sum)
	if errors.Is(err, ErrNotFound) {
		return nil, 0, "", false
	}
	if err != nil {
		log.Printf("session lookup failed: %v, path=%s", err, req.URL.Path)
		return nil, 0, "", false
	}
	return usr, sid, cookie.Value, true
}

// RequireAuth wraps a handler that needs a valid session. Every read route
// and every write route of the platform uses this wrapper, because a member
// must sign in to read a post and to write a post.
func (app *App) RequireAuth(hnd AuthHandler) http.HandlerFunc {
	return func(res http.ResponseWriter, req *http.Request) {
		usr, sid, raw, found := app.SessionFrom(req)
		if !found {
			app.ClearSessionCookie(res)
			http.Redirect(res, req, "/", http.StatusSeeOther)
			return
		}

		req = WithCSRF(req, app.CSRFFor(raw))
		hnd(res, req, usr, raw)

		// The write of the last-seen time happens after the response, and
		// at most one time in each cache window.
		if app.seen.Should(sid) {
			if err := app.TouchSession(sid); err != nil {
				log.Printf("touch session %d: %v", sid, err)
			}
		}
	}
}

// CSRFFor derives the form token from the session token. The method needs
// no table and no server state, because the process secret and the session
// token are sufficient.
func (app *App) CSRFFor(raw string) string {
	mac := hmac.New(sha256.New, app.secret)
	mac.Write([]byte(raw))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// CheckCSRF compares the form field with the derived token.
func (app *App) CheckCSRF(req *http.Request, raw string) bool {
	sent := req.PostFormValue("csrf")
	if sent == "" {
		return false
	}
	want := app.CSRFFor(raw)
	return hmac.Equal([]byte(sent), []byte(want))
}

// StartLogin makes a login token and sends the mail. The function reports
// no result to the caller, because the response text must be the same for a
// known address and for an unknown address.
func (app *App) StartLogin(email string) {
	usr, err := app.FindUserByEmail(email)
	if errors.Is(err, ErrNotFound) {
		return
	}
	if err != nil {
		log.Printf("login lookup failed: %v", err)
		return
	}
	if !usr.Enabled {
		return
	}

	raw, sum := NewToken()
	if err := app.CreateLoginToken(usr.ID, sum, loginTokenLife); err != nil {
		log.Printf("create login token for %d: %v", usr.ID, err)
		return
	}

	// The mail send runs in its own goroutine, so that a slow relay does
	// not hold the HTTP response.
	go func(addr, token string) {
		if err := app.SendLoginMail(addr, token); err != nil {
			log.Printf("send login mail failed: %v", err)
		}
	}(usr.Email, raw)
}

// FinishLogin consumes a login token and makes a session. The result is the
// raw session token.
func (app *App) FinishLogin(res http.ResponseWriter, req *http.Request, token string) (string, error) {
	sum, valid := HashToken(token)
	if !valid {
		return "", ErrNotFound
	}
	uid, err := app.ConsumeLoginToken(sum)
	if err != nil {
		return "", err
	}

	geo := app.GeoTable()
	addr := geo.ClientAddr(req).String()
	agent := req.Header.Get("User-Agent")

	raw, sessionSum := NewToken()
	life := time.Duration(app.Conf().SessionDays) * 24 * time.Hour
	if _, err := app.CreateSession(uid, sessionSum, addr, agent, life); err != nil {
		return "", err
	}
	app.SetSessionCookie(res, raw)
	return raw, nil
}

