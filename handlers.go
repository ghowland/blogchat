package main

import (
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// maxFormBytes limits the size of a request body.
const maxFormBytes = 32 << 10

// parseForm reads the form and applies the size limit.
func parseForm(res http.ResponseWriter, req *http.Request) error {
	req.Body = http.MaxBytesReader(res, req.Body, maxFormBytes)
	return req.ParseForm()
}

// timeText makes the display form of a Unix time value.
func timeText(unix int64) string {
	return time.Unix(unix, 0).UTC().Format("2006-01-02 15:04")
}

// ---------- public pages ----------

// ShowLogin is the entry page. A member with a session goes to the feed.
func (app *App) ShowLogin(res http.ResponseWriter, req *http.Request) {
	if req.URL.Path != "/" {
		app.Fail(res, req, http.StatusNotFound, "page not found")
		return
	}
	if _, _, _, found := app.SessionFrom(req); found {
		http.Redirect(res, req, "/feed", http.StatusSeeOther)
		return
	}
	ctx := app.Base(req, nil)
	ctx["sent"] = false
	ctx["error"] = ""
	app.Render(res, req, "login", ctx)
}

// SubmitLogin accepts an email address and sends a sign-in link.
// The response is the same for a known address and for an unknown address,
// so that the page does not disclose the member list.
func (app *App) SubmitLogin(res http.ResponseWriter, req *http.Request) {
	if err := parseForm(res, req); err != nil {
		app.Fail(res, req, http.StatusBadRequest, "the request is not valid")
		return
	}

	ctx := app.Base(req, nil)
	ctx["sent"] = false

	email, err := ValidEmail(req.PostFormValue("email"))
	if err != nil {
		ctx["error"] = err.Error()
		app.Render(res, req, "login", ctx)
		return
	}

	geo := app.GeoTable()
	addr := geo.ClientAddr(req).String()
	if !app.limits.Allow("login-ip:"+addr, loginRateLimit, loginRateWindow) ||
		!app.limits.Allow("login-mail:"+email, loginRateLimit, loginRateWindow) {
		ctx["error"] = "too many requests, wait 15 minutes"
		app.Render(res, req, "login", ctx)
		return
	}

	app.StartLogin(email)

	ctx["sent"] = true
	ctx["error"] = ""
	app.Render(res, req, "login", ctx)
}

// ConsumeLink is the target of the sign-in link. A success makes a session
// and moves to the key list, so that the member sees the active keys at
// once and can remove the other keys.
func (app *App) ConsumeLink(res http.ResponseWriter, req *http.Request) {
	token := req.PathValue("token")
	if token == "" {
		app.Fail(res, req, http.StatusNotFound, "page not found")
		return
	}

	geo := app.GeoTable()
	addr := geo.ClientAddr(req).String()
	if !app.limits.Allow("link-ip:"+addr, 20, loginRateWindow) {
		app.Fail(res, req, http.StatusTooManyRequests, "too many requests")
		return
	}

	if _, err := app.FinishLogin(res, req, token); err != nil {
		if errors.Is(err, ErrNotFound) {
			ctx := app.Base(req, nil)
			ctx["sent"] = false
			ctx["error"] = "the link is used, or expired. Request a new link."
			app.Render(res, req, "login", ctx)
			return
		}
		log.Printf("finish login: %v", err)
		app.Fail(res, req, http.StatusInternalServerError, "server error")
		return
	}
	http.Redirect(res, req, "/keys", http.StatusSeeOther)
}

// ShowTerms is available without a session, because a person must read the
// terms before the first sign-in.
func (app *App) ShowTerms(res http.ResponseWriter, req *http.Request) {
	usr, _, raw, found := app.SessionFrom(req)
	if found {
		req = WithCSRF(req, app.CSRFFor(raw))
	} else {
		usr = nil
	}
	app.Render(res, req, "terms", app.Base(req, usr))
}

// ---------- feed and posts ----------

// ShowFeed lists the blog threads, newest first.
func (app *App) ShowFeed(res http.ResponseWriter, req *http.Request, usr *User, raw string) {
	app.renderList(res, req, usr, false)
}

// ShowChats lists the chat channels, most recent activity first.
func (app *App) ShowChats(res http.ResponseWriter, req *http.Request, usr *User, raw string) {
	app.renderList(res, req, usr, true)
}

// renderList serves both list pages. One function keeps the pagination
// behaviour the same for the two kinds.
func (app *App) renderList(res http.ResponseWriter, req *http.Request, usr *User, chat bool) {
	conf := app.Conf()
	page := 1
	if text := req.URL.Query().Get("page"); text != "" {
		if num, err := strconv.Atoi(text); err == nil && num > 0 {
			page = num
		}
	}
	offset := (page - 1) * conf.PostsPerPage

	rows, err := app.ListPosts(chat, conf.PostsPerPage, offset)
	if err != nil {
		log.Printf("list posts chat=%v: %v", chat, err)
		app.Fail(res, req, http.StatusInternalServerError, "server error")
		return
	}
	total, err := app.CountPosts(chat)
	if err != nil {
		log.Printf("count posts chat=%v: %v", chat, err)
		app.Fail(res, req, http.StatusInternalServerError, "server error")
		return
	}

	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		when := timeText(row.CreatedAt)
		if chat {
			when = timeText(row.LastAt)
		}
		items = append(items, map[string]any{
			"id":      row.ID,
			"subject": row.Subject,
			"handle":  row.Handle,
			"when":    when,
			"replies": row.Replies,
		})
	}

	base := "/feed"
	name := "feed"
	if chat {
		base = "/chat"
		name = "chats"
	}

	ctx := app.Base(req, usr)
	ctx["posts"] = items
	ctx["empty"] = len(items) == 0
	ctx["chat"] = chat
	ctx["base"] = base
	ctx["keep"] = conf.ChatKeep
	ctx["page"] = page
	ctx["has_prev"] = page > 1
	ctx["prev_page"] = page - 1
	ctx["has_next"] = offset+len(rows) < total
	ctx["next_page"] = page + 1
	ctx["error"] = req.URL.Query().Get("error")
	app.Render(res, req, name, ctx)
}

// ShowPost renders one thread with its replies.
func (app *App) ShowPost(res http.ResponseWriter, req *http.Request, usr *User, raw string) {
	pid, err := strconv.ParseInt(req.PathValue("id"), 10, 64)
	if err != nil || pid < 1 {
		app.Fail(res, req, http.StatusNotFound, "the post does not exist")
		return
	}

	pst, err := app.GetPost(pid)
	if errors.Is(err, ErrNotFound) {
		app.Fail(res, req, http.StatusNotFound, "the post does not exist")
		return
	}
	if err != nil {
		log.Printf("get post %d: %v", pid, err)
		app.Fail(res, req, http.StatusInternalServerError, "server error")
		return
	}
	// A channel needs the chat page, because the blog page shows every reply
	// with no limit. The identifier is valid, so the answer is a redirect
	// and not an error.
	if pst.IsChat {
		http.Redirect(res, req, "/c/"+strconv.FormatInt(pid, 10), http.StatusSeeOther)
		return
	}

	replies, err := app.ListReplies(pid)

	if err != nil {
		log.Printf("list replies %d: %v", pid, err)
		app.Fail(res, req, http.StatusInternalServerError, "server error")
		return
	}

	owner := pst.UserID == usr.ID
	items := make([]map[string]any, 0, len(replies))
	for _, rep := range replies {
		items = append(items, map[string]any{
			"id":         rep.ID,
			"handle":     rep.Handle,
			"body":       rep.Body,
			"when":       timeText(rep.CreatedAt),
			"can_delete": owner || rep.UserID == usr.ID,
		})
	}

	ctx := app.Base(req, usr)
	ctx["post"] = map[string]any{
		"id":      pst.ID,
		"subject": pst.Subject,
		"body":    pst.Body,
		"handle":  pst.Handle,
		"when":    timeText(pst.CreatedAt),
	}
	ctx["replies"] = items
	ctx["reply_count"] = len(items)
	ctx["owner"] = owner
	ctx["error"] = req.URL.Query().Get("error")
	app.Render(res, req, "post", ctx)
}

// CreatePostHandler adds a thread. The member becomes the owner.
func (app *App) CreatePostHandler(res http.ResponseWriter, req *http.Request, usr *User, raw string) {
	if err := parseForm(res, req); err != nil {
		app.Fail(res, req, http.StatusBadRequest, "the request is too large")
		return
	}
	if !app.CheckCSRF(req, raw) {
		app.Fail(res, req, http.StatusForbidden, "the form is expired, try again")
		return
	}
	if !app.limits.Allow("post:"+strconv.FormatInt(usr.ID, 10), 10, time.Minute) {
		app.redirectError(res, req, "/feed", "too many posts, wait one minute")
		return
	}

	subject, err := ValidSubject(req.PostFormValue("subject"))
	if err != nil {
		app.redirectError(res, req, "/feed", err.Error())
		return
	}
	body, err := ValidBody(req.PostFormValue("body"), maxPostBody)
	if err != nil {
		app.redirectError(res, req, "/feed", err.Error())
		return
	}

	pid, err := app.CreatePost(usr.ID, subject, body, false)
	if err != nil {
		log.Printf("create post: %v", err)
		app.Fail(res, req, http.StatusInternalServerError, "server error")
		return
	}
	http.Redirect(res, req, "/p/"+strconv.FormatInt(pid, 10), http.StatusSeeOther)
}

// DeletePostHandler removes a thread. Only the owner can do this.
func (app *App) DeletePostHandler(res http.ResponseWriter, req *http.Request, usr *User, raw string) {
	if err := parseForm(res, req); err != nil {
		app.Fail(res, req, http.StatusBadRequest, "the request is not valid")
		return
	}
	if !app.CheckCSRF(req, raw) {
		app.Fail(res, req, http.StatusForbidden, "the form is expired, try again")
		return
	}
	pid, err := strconv.ParseInt(req.PathValue("id"), 10, 64)
	if err != nil || pid < 1 {
		app.Fail(res, req, http.StatusNotFound, "the post does not exist")
		return
	}

	// The list target comes from the form, because the row is gone after
	// the delete and the kind cannot be read from the database.
	target := "/feed"
	if req.PostFormValue("kind") == "chat" {
		target = "/chat"
	}

	if err := app.DeletePost(pid, usr.ID); err != nil {
		if errors.Is(err, ErrNotFound) {
			app.Fail(res, req, http.StatusForbidden, "you do not own this post")
			return
		}
		log.Printf("delete post %d: %v", pid, err)
		app.Fail(res, req, http.StatusInternalServerError, "server error")
		return
	}
	http.Redirect(res, req, target, http.StatusSeeOther)
}

// ---------- replies ----------

// CreateReplyHandler adds a comment to a thread.
func (app *App) CreateReplyHandler(res http.ResponseWriter, req *http.Request, usr *User, raw string) {
	if err := parseForm(res, req); err != nil {
		app.Fail(res, req, http.StatusBadRequest, "the request is too large")
		return
	}
	if !app.CheckCSRF(req, raw) {
		app.Fail(res, req, http.StatusForbidden, "the form is expired, try again")
		return
	}
	pid, err := strconv.ParseInt(req.PathValue("id"), 10, 64)
	if err != nil || pid < 1 {
		app.Fail(res, req, http.StatusNotFound, "the post does not exist")
		return
	}
	target := "/p/" + strconv.FormatInt(pid, 10)

	if !app.limits.Allow("reply:"+strconv.FormatInt(usr.ID, 10), 20, time.Minute) {
		app.redirectError(res, req, target, "too many replies, wait one minute")
		return
	}

	body, err := ValidBody(req.PostFormValue("body"), maxReplyBody)
	if err != nil {
		app.redirectError(res, req, target, err.Error())
		return
	}

	pst, err := app.GetPost(pid)
	if err != nil {
		app.Fail(res, req, http.StatusNotFound, "the post does not exist")
		return
	}
	if pst.IsChat {
		app.Fail(res, req, http.StatusBadRequest, "that is a channel, not a post")
		return
	}
	if _, err := app.CreateReply(pid, usr.ID, body); err != nil {
		log.Printf("create reply on %d: %v", pid, err)
		app.Fail(res, req, http.StatusInternalServerError, "server error")
		return
	}
	http.Redirect(res, req, target, http.StatusSeeOther)
}

// DeleteReplyHandler removes a comment. The author of the comment can do
// this, and the owner of the thread can do this.
func (app *App) DeleteReplyHandler(res http.ResponseWriter, req *http.Request, usr *User, raw string) {
	if err := parseForm(res, req); err != nil {
		app.Fail(res, req, http.StatusBadRequest, "the request is not valid")
		return
	}
	if !app.CheckCSRF(req, raw) {
		app.Fail(res, req, http.StatusForbidden, "the form is expired, try again")
		return
	}
	rid, err := strconv.ParseInt(req.PathValue("id"), 10, 64)
	if err != nil || rid < 1 {
		app.Fail(res, req, http.StatusNotFound, "the reply does not exist")
		return
	}

	target := "/feed"
	prefix := "/p/"
	if req.PostFormValue("kind") == "chat" {
		target = "/chat"
		prefix = "/c/"
	}
	if back := req.PostFormValue("post"); back != "" {
		if pid, err := strconv.ParseInt(back, 10, 64); err == nil && pid > 0 {
			target = prefix + strconv.FormatInt(pid, 10)
		}
	}

	if err := app.DeleteReply(rid, usr.ID); err != nil {
		if errors.Is(err, ErrNotFound) {
			app.Fail(res, req, http.StatusForbidden, "you cannot delete this reply")
			return
		}
		log.Printf("delete reply %d: %v", rid, err)
		app.Fail(res, req, http.StatusInternalServerError, "server error")
		return
	}
	http.Redirect(res, req, target, http.StatusSeeOther)
}

// ---------- chat ----------

// ShowChannel renders one channel with its newest messages.
func (app *App) ShowChannel(res http.ResponseWriter, req *http.Request, usr *User, raw string) {
	conf := app.Conf()
	pid, err := strconv.ParseInt(req.PathValue("id"), 10, 64)
	if err != nil || pid < 1 {
		app.Fail(res, req, http.StatusNotFound, "the channel does not exist")
		return
	}

	pst, err := app.GetPost(pid)
	if errors.Is(err, ErrNotFound) {
		app.Fail(res, req, http.StatusNotFound, "the channel does not exist")
		return
	}
	if err != nil {
		log.Printf("get channel %d: %v", pid, err)
		app.Fail(res, req, http.StatusInternalServerError, "server error")
		return
	}
	// A blog post needs the blog page. The redirect makes a wrong link
	// self-correcting in both directions.
	if !pst.IsChat {
		http.Redirect(res, req, "/p/"+strconv.FormatInt(pid, 10), http.StatusSeeOther)
		return
	}

	lines, err := app.ListChatLines(pid, conf.ChatPerPage)
	if err != nil {
		log.Printf("list chat lines %d: %v", pid, err)
		app.Fail(res, req, http.StatusInternalServerError, "server error")
		return
	}

	owner := pst.UserID == usr.ID
	items := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		items = append(items, map[string]any{
			"id":         line.ID,
			"handle":     line.Handle,
			"body":       line.Body,
			"when":       timeText(line.CreatedAt),
			"can_delete": owner || line.UserID == usr.ID,
		})
	}

	ctx := app.Base(req, usr)
	ctx["post"] = map[string]any{
		"id":      pst.ID,
		"subject": pst.Subject,
		"body":    pst.Body,
		"handle":  pst.Handle,
		"when":    timeText(pst.CreatedAt),
	}
	ctx["has_topic"] = pst.Body != ""
	ctx["lines"] = items
	ctx["line_count"] = len(items)
	ctx["empty"] = len(items) == 0
	ctx["owner"] = owner
	ctx["keep"] = conf.ChatKeep
	ctx["error"] = req.URL.Query().Get("error")
	app.Render(res, req, "chat", ctx)
}

// CreateChannelHandler makes a channel. The subject is the channel name and
// the body is the optional topic line.
func (app *App) CreateChannelHandler(res http.ResponseWriter, req *http.Request, usr *User, raw string) {
	if err := parseForm(res, req); err != nil {
		app.Fail(res, req, http.StatusBadRequest, "the request is too large")
		return
	}
	if !app.CheckCSRF(req, raw) {
		app.Fail(res, req, http.StatusForbidden, "the form is expired, try again")
		return
	}
	if !app.limits.Allow("channel:"+strconv.FormatInt(usr.ID, 10), 5, time.Minute) {
		app.redirectError(res, req, "/chat", "too many channels, wait one minute")
		return
	}

	subject, err := ValidSubject(req.PostFormValue("subject"))
	if err != nil {
		app.redirectError(res, req, "/chat", err.Error())
		return
	}
	topic, err := ValidTopic(req.PostFormValue("body"))
	if err != nil {
		app.redirectError(res, req, "/chat", err.Error())
		return
	}

	pid, err := app.CreatePost(usr.ID, subject, topic, true)
	if err != nil {
		log.Printf("create channel: %v", err)
		app.Fail(res, req, http.StatusInternalServerError, "server error")
		return
	}
	http.Redirect(res, req, "/c/"+strconv.FormatInt(pid, 10), http.StatusSeeOther)
}

// CreateChatLineHandler adds one message and trims the channel to the keep
// limit from the configuration.
func (app *App) CreateChatLineHandler(res http.ResponseWriter, req *http.Request, usr *User, raw string) {
	if err := parseForm(res, req); err != nil {
		app.Fail(res, req, http.StatusBadRequest, "the request is too large")
		return
	}
	if !app.CheckCSRF(req, raw) {
		app.Fail(res, req, http.StatusForbidden, "the form is expired, try again")
		return
	}
	pid, err := strconv.ParseInt(req.PathValue("id"), 10, 64)
	if err != nil || pid < 1 {
		app.Fail(res, req, http.StatusNotFound, "the channel does not exist")
		return
	}
	target := "/c/" + strconv.FormatInt(pid, 10)

	// Chat is a faster interaction than the blog, so the limit is higher
	// and the counter is separate.
	if !app.limits.Allow("chat:"+strconv.FormatInt(usr.ID, 10), 60, time.Minute) {
		app.redirectError(res, req, target, "too many messages, wait one minute")
		return
	}

	body, err := ValidBody(req.PostFormValue("body"), maxChatBody)
	if err != nil {
		app.redirectError(res, req, target, err.Error())
		return
	}

	pst, err := app.GetPost(pid)
	if err != nil {
		app.Fail(res, req, http.StatusNotFound, "the channel does not exist")
		return
	}
	if !pst.IsChat {
		app.Fail(res, req, http.StatusBadRequest, "that is a post, not a channel")
		return
	}

	if _, err := app.CreateChatLine(pid, usr.ID, body, app.Conf().ChatKeep); err != nil {
		log.Printf("create chat line on %d: %v", pid, err)
		app.Fail(res, req, http.StatusInternalServerError, "server error")
		return
	}
	http.Redirect(res, req, target, http.StatusSeeOther)
}

// ---------- keys ----------

// ShowKeys lists the active sessions of the member.
func (app *App) ShowKeys(res http.ResponseWriter, req *http.Request, usr *User, raw string) {
	_, current, _, _ := app.SessionFrom(req)

	list, err := app.ListSessions(usr.ID, current)
	if err != nil {
		log.Printf("list sessions for %d: %v", usr.ID, err)
		app.Fail(res, req, http.StatusInternalServerError, "server error")
		return
	}

	items := make([]map[string]any, 0, len(list))
	others := 0
	for _, ses := range list {
		if !ses.Current {
			others++
		}
		items = append(items, map[string]any{
			"id":      ses.ID,
			"when":    timeText(ses.CreatedAt),
			"last":    timeText(ses.LastSeen),
			"ip":      ses.IP,
			"agent":   ses.Agent,
			"current": ses.Current,
		})
	}

	inviter := usr.InviterHandle
	if inviter == "" {
		inviter = "founder"
	}

	ctx := app.Base(req, usr)
	ctx["sessions"] = items
	ctx["others"] = others
	ctx["has_others"] = others > 0
	ctx["inviter"] = inviter
	ctx["joined"] = timeText(usr.CreatedAt)
	ctx["notice"] = req.URL.Query().Get("notice")
	app.Render(res, req, "keys", ctx)
}

// RevokeOtherKeys removes every session except the current one. The other
// devices then need a new sign-in link.
func (app *App) RevokeOtherKeys(res http.ResponseWriter, req *http.Request, usr *User, raw string) {
	if err := parseForm(res, req); err != nil {
		app.Fail(res, req, http.StatusBadRequest, "the request is not valid")
		return
	}
	if !app.CheckCSRF(req, raw) {
		app.Fail(res, req, http.StatusForbidden, "the form is expired, try again")
		return
	}

	_, current, _, found := app.SessionFrom(req)
	if !found {
		http.Redirect(res, req, "/", http.StatusSeeOther)
		return
	}

	count, err := app.DeleteOtherSessions(usr.ID, current)
	if err != nil {
		log.Printf("revoke keys for %d: %v", usr.ID, err)
		app.Fail(res, req, http.StatusInternalServerError, "server error")
		return
	}
	http.Redirect(res, req,
		"/keys?notice="+strconv.FormatInt(count, 10)+" other keys removed",
		http.StatusSeeOther)
}

// Logout removes the current session only.
func (app *App) Logout(res http.ResponseWriter, req *http.Request, usr *User, raw string) {
	if err := parseForm(res, req); err != nil {
		app.Fail(res, req, http.StatusBadRequest, "the request is not valid")
		return
	}
	if !app.CheckCSRF(req, raw) {
		app.Fail(res, req, http.StatusForbidden, "the form is expired, try again")
		return
	}

	_, current, _, found := app.SessionFrom(req)
	if found {
		if err := app.DeleteSession(usr.ID, current); err != nil {
			log.Printf("logout %d: %v", current, err)
		}
		app.seen.Forget(current)
	}
	app.ClearSessionCookie(res)
	http.Redirect(res, req, "/", http.StatusSeeOther)
}

// ---------- invites ----------

// ShowInvite renders the invite form with the remaining quota.
func (app *App) ShowInvite(res http.ResponseWriter, req *http.Request, usr *User, raw string) {
	app.renderInvite(res, req, usr, "", false)
}

// SubmitInvite makes a member row and sends the first sign-in link. This
// operation is the whole of the sign-up process, because the platform has
// no registration page.
func (app *App) SubmitInvite(res http.ResponseWriter, req *http.Request, usr *User, raw string) {
	if err := parseForm(res, req); err != nil {
		app.Fail(res, req, http.StatusBadRequest, "the request is not valid")
		return
	}
	if !app.CheckCSRF(req, raw) {
		app.Fail(res, req, http.StatusForbidden, "the form is expired, try again")
		return
	}

	conf := app.Conf()
	open, err := app.CountOpenInvites(usr.ID)
	if err != nil {
		log.Printf("count invites for %d: %v", usr.ID, err)
		app.Fail(res, req, http.StatusInternalServerError, "server error")
		return
	}
	if open >= conf.InviteQuota {
		app.renderInvite(res, req, usr, "your open invites are at the limit", false)
		return
	}

	email, err := ValidEmail(req.PostFormValue("email"))
	if err != nil {
		app.renderInvite(res, req, usr, err.Error(), false)
		return
	}
	handle, err := ValidHandle(req.PostFormValue("handle"))
	if err != nil {
		app.renderInvite(res, req, usr, err.Error(), false)
		return
	}

	newID, err := app.CreateUser(email, handle, usr.ID)
	if err != nil {
		// The email column and the handle column are unique, so a duplicate
		// makes a constraint error. The message stays the same for both
		// cases, so that the page does not disclose the member list.
		if strings.Contains(strings.ToUpper(err.Error()), "UNIQUE") {
			app.renderInvite(res, req, usr,
				"that address or that handle is not available", false)
			return
		}
		log.Printf("create user: %v", err)
		app.Fail(res, req, http.StatusInternalServerError, "server error")
		return
	}

	token, sum := NewToken()
	if err := app.CreateLoginToken(newID, sum, inviteTokenLife); err != nil {
		log.Printf("create invite token for %d: %v", newID, err)
		app.Fail(res, req, http.StatusInternalServerError, "server error")
		return
	}

	go func(addr, name, from, link string) {
		if err := app.SendInviteMail(addr, name, from, link); err != nil {
			log.Printf("send invite mail failed: %v", err)
		}
	}(email, handle, usr.Handle, token)

	app.renderInvite(res, req, usr, "", true)
}

func (app *App) renderInvite(res http.ResponseWriter, req *http.Request, usr *User, msg string, sent bool) {
	open, err := app.CountOpenInvites(usr.ID)
	if err != nil {
		log.Printf("count invites for %d: %v", usr.ID, err)
		open = 0
	}
	remaining := app.Conf().InviteQuota - open
	if remaining < 0 {
		remaining = 0
	}

	ctx := app.Base(req, usr)
	ctx["error"] = msg
	ctx["sent"] = sent
	ctx["remaining"] = remaining
	ctx["can_invite"] = remaining > 0
	app.Render(res, req, "invite", ctx)
}

// redirectError returns to a page with a short message in the query.
func (app *App) redirectError(res http.ResponseWriter, req *http.Request, target, msg string) {
	sep := "?"
	if strings.Contains(target, "?") {
		sep = "&"
	}
	http.Redirect(res, req, target+sep+"error="+urlValue(msg), http.StatusSeeOther)
}

// urlValue encodes one query value.
func urlValue(text string) string {
	var buf strings.Builder
	for idx := 0; idx < len(text); idx++ {
		chr := text[idx]
		safe := (chr >= 'a' && chr <= 'z') || (chr >= 'A' && chr <= 'Z') ||
			(chr >= '0' && chr <= '9') || chr == '-' || chr == '_' || chr == '.'
		if safe {
			buf.WriteByte(chr)
			continue
		}
		if chr == ' ' {
			buf.WriteByte('+')
			continue
		}
		buf.WriteString("%")
		const hex = "0123456789ABCDEF"
		buf.WriteByte(hex[chr>>4])
		buf.WriteByte(hex[chr&0x0F])
	}
	return buf.String()
}
