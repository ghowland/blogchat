# blogchat — Technical Specification

**Version 1.1** · A minimal invite-only blog and chat platform

*Supersedes v1.0. Changes in this revision: source relocated to `code/`, templates renamed to `template/`, hand-written CSS replaced by Tailwind delivered from a CDN, and URL linkification added to post, reply, and chat message bodies.*

---

## Table of contents

1. [What blogchat is](#1-what-blogchat-is)
2. [Design principles](#2-design-principles)
3. [Concepts](#3-concepts)
4. [Architecture](#4-architecture)
5. [Data model](#5-data-model)
6. [Authentication](#6-authentication)
7. [Session keys](#7-session-keys)
8. [Invitations](#8-invitations)
9. [Posts](#9-posts)
10. [Chat](#10-chat)
11. [Body rendering and linkification](#11-body-rendering-and-linkification)
12. [Geoblocking](#12-geoblocking)
13. [HTTP surface](#13-http-surface)
14. [Templates and styling](#14-templates-and-styling)
15. [Client-side behaviour](#15-client-side-behaviour)
16. [Security model](#16-security-model)
17. [Configuration](#17-configuration)
18. [Source layout](#18-source-layout)
19. [Runtime behaviour](#19-runtime-behaviour)
20. [Deployment](#20-deployment)
21. [Operations](#21-operations)
22. [Limits and constants](#22-limits-and-constants)
23. [Known limitations](#23-known-limitations)
24. [Glossary](#24-glossary)

---

## 1. What blogchat is

blogchat is a text-only, invite-only, private community site. It has two areas: a blog where members write posts with a subject and a body, and a chat where members send short messages in named channels. Both areas require a sign-in to read as well as to write; nothing is public.

The whole system is one Go program and one SQLite database file. There are no passwords, no registration page, no external services at runtime, and no framework.

**Who it is for.** A small closed group — a family, a club, a project team, a set of friends — that wants a private place to write and talk. It is designed for tens of members, not thousands.

**What it is not.** It is not a public blog, not a forum with anonymous readers, not a Slack replacement with file uploads and notifications, and not a system that scales horizontally. Each of those is a deliberate exclusion, not a missing feature.

---

## 2. Design principles

These five principles explain nearly every decision in this document.

**One process, one file.** All state lives in one SQLite database on one disk, accessed by one process through one connection. This removes every class of distributed-systems problem: no cache invalidation between machines, no distributed locking, no connection pool contention, no database server to operate. The cost is that the system cannot run two instances.

**No passwords.** The system never stores, transmits, or verifies a password. Identity is proven by control of an email address. This removes password storage, reset flows, credential stuffing, and the responsibility of protecting a credential database.

**Text only.** Posts and messages are plain text. There is no Markdown, no HTML input, no image upload, no file attachment. The one exception is automatic URL detection, described in §11, which is performed in Go rather than by interpreting member markup.

**Server-rendered HTML.** Pages are complete HTML documents built on the server. There is no client-side framework, no JSON API for the browser, and no build step. Interactivity is added by HTMX attributes on the HTML itself.

**Everything in one binary.** Templates and the client script are compiled into the executable with Go's `embed` feature. Deployment is one file plus one database. Styling arrives from a CDN at page load and is the only asset not embedded.

---

## 3. Concepts

Seven terms a new reader needs before the rest of the document.

**Member.** A person with an account. Every member has an email address, which is private and never shown, and a handle, which is public and shown on everything they write.

**Handle.** The public display name, 2 to 24 characters, letters and digits and the signs `_`, `-`, `.`. Set when the member is invited; cannot be changed afterwards.

**Invitation.** The only way to join. An existing member enters an email address and a handle; this creates the account immediately and sends a sign-in link. There is no registration page and no code to redeem.

**Sign-in link.** A one-time URL sent by email, valid for 15 minutes. Opening it creates a session. This is the only authentication mechanism.

**Key.** A session. Each device that signs in gets its own key. A member can see all their keys and remove every key except the current one, forcing the other devices to sign in again.

**Post.** A blog thread with a subject and a body. The member who wrote it owns it and can delete it. Other members write replies, which have a body only.

**Channel.** A chat room with a name and an optional topic. Messages inside it are short and the channel keeps only the newest 500; older messages are deleted permanently.

A post and a channel are the same kind of database row distinguished by one flag. A reply and a chat message are likewise the same kind of row. See §5.

---

## 4. Architecture

### 4.1 The whole system

```
                     Internet
                        │
                    443 │ HTTPS
                        ▼
               ┌─────────────────┐
               │      Caddy      │  certificate, TLS termination
               └────────┬────────┘
                        │ 8080 HTTP
                        ▼
               ┌─────────────────┐
               │  blogchat (Go)  │  one process
               │                 │
               │  geoblock       │  middleware, runs first
               │  router         │  net/http ServeMux
               │  handlers       │  session check, then the work
               │  templates      │  mustache, embedded
               └────────┬────────┘
                        │ one connection
                        ▼
               ┌─────────────────┐
               │  /data/blog.db  │  SQLite, WAL mode
               └─────────────────┘
```

Caddy is a separate container that obtains and renews a TLS certificate from Let's Encrypt automatically and forwards decrypted requests to the program. The program itself speaks plain HTTP and knows nothing about certificates.

### 4.2 Request path

Every request passes through the same sequence:

1. **Geoblock middleware.** Determines the country of the client and returns 403 if it is on the block list. Runs before the router and before any database access, so a blocked request costs almost nothing. The path `/healthz` is exempt.
2. **Request log.** One line per request. Sign-in link paths are rewritten to `/l/[token]` so the secret never reaches the log.
3. **Router.** Go's standard `ServeMux` with method and wildcard patterns, available since Go 1.22.
4. **Authentication wrapper.** All routes except five require a valid session. The wrapper looks up the session, computes a CSRF token, and passes both to the handler.
5. **Handler.** Validates input, calls the database, renders a template.
6. **Last-seen update.** After the response, the session's last-seen time is written, at most once per minute per session.

### 4.3 Concurrency

Go's HTTP server handles each request in its own goroutine. The database has one connection with `SetMaxOpenConns(1)`, so all database access is serialised by the connection pool. This means:

- No `SQLITE_BUSY` errors are possible.
- No transaction can deadlock with another.
- Database access is a queue; a slow query blocks others.

For the intended scale the serialisation is not a bottleneck. Every query in the system completes in well under a millisecond.

Shared in-memory state uses `atomic.Pointer` for the configuration and the geo table, and a mutex-protected map for rate limits.

---

## 5. Data model

Five tables. All identifiers are 64-bit integers; all timestamps are Unix seconds.

### 5.1 users

```sql
CREATE TABLE users (
    id          INTEGER PRIMARY KEY,
    email       TEXT    NOT NULL UNIQUE,   -- private, never rendered
    handle      TEXT    NOT NULL UNIQUE,   -- public
    invited_by  INTEGER REFERENCES users(id),
    created_at  INTEGER NOT NULL,
    last_login  INTEGER,                   -- NULL until first sign-in
    enabled     INTEGER NOT NULL DEFAULT 1
);
```

`invited_by` is NULL for exactly one row: the root member. The interface shows "founder" for that member.

`last_login` records the first successful sign-in, and its NULL state marks an invitation as still open for the quota calculation.

`enabled` set to 0 revokes access on the next request. This is the correct way to remove a member; deleting the row cascades and destroys all their content.

### 5.2 login_tokens

```sql
CREATE TABLE login_tokens (
    id          INTEGER PRIMARY KEY,
    user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash  BLOB    NOT NULL UNIQUE,   -- SHA-256 of 32 random bytes
    created_at  INTEGER NOT NULL,
    expires_at  INTEGER NOT NULL,
    used_at     INTEGER                    -- NULL until consumed
);
```

The raw token exists only in the email. The database stores its hash. Single-use and time-limited.

### 5.3 sessions

```sql
CREATE TABLE sessions (
    id          INTEGER PRIMARY KEY,
    user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash  BLOB    NOT NULL UNIQUE,
    created_at  INTEGER NOT NULL,
    last_seen   INTEGER NOT NULL,
    expires_at  INTEGER NOT NULL,
    ip          TEXT    NOT NULL,
    agent       TEXT    NOT NULL           -- truncated to 120 characters
);
```

`ip` and `agent` exist solely so the member can recognise their own devices on the keys page.

### 5.4 posts

```sql
CREATE TABLE posts (
    id          INTEGER PRIMARY KEY,
    user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    subject     TEXT    NOT NULL,
    body        TEXT    NOT NULL,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,
    is_chat     INTEGER NOT NULL DEFAULT 0,
    last_at     INTEGER NOT NULL DEFAULT 0
);
```

**This table holds both blog posts and chat channels.**

| Column | Blog post (`is_chat = 0`) | Channel (`is_chat = 1`) |
|---|---|---|
| `subject` | The post title | The channel name |
| `body` | The post text | The topic line, may be empty |
| `created_at` | Sort key for the feed | Creation time |
| `last_at` | Unused | Time of newest message; sort key for the channel list |

`last_at` is separate from `updated_at` deliberately. `updated_at` means "the post text changed"; `last_at` means "activity occurred". A reply does not change the post text.

`last_at` exists as a stored column rather than being computed because the channel list sorts by most recent activity. A correlated subquery would prevent SQLite from using an index for the sort. With the column, the sort is a single index scan.

### 5.5 replies

```sql
CREATE TABLE replies (
    id          INTEGER PRIMARY KEY,
    post_id     INTEGER NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
    user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    body        TEXT    NOT NULL,
    created_at  INTEGER NOT NULL
);
```

Holds both blog replies and chat messages. Which it is depends on the `is_chat` flag of its parent post. Blog replies are kept forever; chat messages are trimmed to the newest 500 per channel.

### 5.6 Indexes

```sql
CREATE INDEX idx_sessions_user   ON sessions(user_id);
CREATE INDEX idx_tokens_user     ON login_tokens(user_id);
CREATE INDEX idx_replies_post    ON replies(post_id, id);
CREATE INDEX idx_posts_kind_time ON posts(is_chat, created_at DESC);
CREATE INDEX idx_posts_kind_last ON posts(is_chat, last_at DESC);
CREATE INDEX idx_users_inviter   ON users(invited_by);
```

`idx_replies_post` on `(post_id, id)` serves three operations: listing a thread's replies, fetching new chat messages after a given id, and the chat trim.

### 5.7 Schema versioning

The schema is applied from a `migrations` slice, with `PRAGMA user_version` recording the position. Each migration runs in its own transaction. Entries are never edited after release; changes are added as new entries.

### 5.8 The shared-table design

Sharing `posts` and `replies` between blog and chat gives one insert path, one ownership rule, one delete-cascade, and one set of indexes.

Its risk: a query that forgets the `is_chat` filter mixes the two areas. `ListPosts` and `CountPosts` filter. `GetPost` deliberately does **not**, because the handler needs to know which kind it found in order to redirect.

Handlers check `IsChat` and redirect: `/p/{id}` on a channel redirects to `/c/{id}`, and `/c/{id}` on a post redirects to `/p/{id}`. A wrong link self-corrects rather than producing a confusing error.

---

## 6. Authentication

### 6.1 The model

Identity is proven by control of an email address, once per session. There is no password at any point.

### 6.2 Sign-in flow

```
1. Visitor opens /
       ↓
2. Enters email address, submits POST /login
       ↓
3. Server looks up the address
   ├── found and enabled → create token, send mail
   └── not found         → do nothing
       ↓
4. Server shows the same confirmation in both cases
       ↓
5. Member opens https://site/l/<token> from the mail
       ↓
6. Server verifies: hash matches, not used, not expired, member enabled
       ↓
7. Server marks token used, sets last_login, creates session, sets cookie
       ↓
8. Redirect to /keys
```

**Step 4 is a security property.** An identical response for known and unknown addresses prevents the page from disclosing who is a member.

**Step 8 is deliberate.** Landing on the keys page means every sign-in shows the member which devices have access, making an unrecognised session visible immediately.

### 6.3 Token handling

Both login tokens and session tokens are generated the same way:

```go
buf := make([]byte, 32)
rand.Read(buf)                      // crypto/rand
digest := sha256.Sum256(buf)
raw := base64.RawURLEncoding.EncodeToString(buf)
```

The raw value goes to the member; the database stores only the digest. To verify, the server hashes the presented value and looks it up by hash — a single unique-index lookup. Because the lookup is by hash equality in the database, no constant-time comparison is needed in Go.

If `crypto/rand` fails, the program panics. It cannot produce a safe token in that state.

### 6.4 Token consumption

Consuming a login token is a transaction where the state check is inside the `UPDATE`:

```sql
UPDATE login_tokens SET used_at = ?
WHERE token_hash = ? AND used_at IS NULL AND expires_at > ?
```

Zero rows affected means the token was already used or has expired. The single-connection design makes a race impossible anyway, but the condition stays in the statement so a future change to the connection limit cannot introduce a defect.

### 6.5 Session cookie

```
Set-Cookie: sid=<raw token>; Path=/; Max-Age=2592000;
            HttpOnly; Secure; SameSite=Lax
```

`Secure` is set only when the configured site URL uses HTTPS, so local HTTP testing works.

### 6.6 Reading a session

On each authenticated request: read the cookie, decode and hash it, then:

```sql
SELECT ses.id, <user columns>
FROM sessions ses
JOIN users usr ON usr.id = ses.user_id
LEFT JOIN users inv ON inv.id = usr.invited_by
WHERE ses.token_hash = ? AND ses.expires_at > ? AND usr.enabled = 1
```

The `enabled = 1` condition means disabling a member revokes every one of their sessions instantly.

### 6.7 Rate limiting

`POST /login` is limited to 5 requests per 15 minutes, counted separately per email address and per client IP. The sign-in link path `/l/{token}` is limited to 20 per 15 minutes per IP.

Limits are held in an in-memory map with a fixed-window counter, cleaned every 5 minutes. This is correct because there is exactly one process.

---

## 7. Session keys

### 7.1 Purpose

Each device that signs in creates a separate session row — a "key". Because a sign-in link works on any device that opens it, a leaked link creates a session the member did not intend. The keys page makes this visible and reversible.

### 7.2 The keys page

`GET /keys` lists every active session of the member, newest first, showing sign-in time, last-seen time, IP address, and truncated user agent, with the current session highlighted. The page also shows the member's handle, who invited them, and when they joined.

### 7.3 Removing other keys

`POST /keys/revoke-others` executes:

```sql
DELETE FROM sessions WHERE user_id = ? AND id <> ?
```

The current session is kept; every other device loses access on its next request. This is the recovery action for a leaked link, a shared computer, or a lost device.

`POST /logout` removes only the current session.

### 7.4 Last-seen updates

Writing `last_seen` on every request would mean a database write per page view. A `SeenCache` holds the last write time per session in memory and permits a write at most once per 60 seconds per session. The displayed time is accurate to within a minute.

---

## 8. Invitations

### 8.1 The model

Creating a member *is* the invitation. There is no pending-invitation table and no acceptance step.

An existing member submits an email address and a handle. The server creates the `users` row immediately with `invited_by` set to the inviter, creates a login token valid for 7 days, and sends a mail containing the handle and the sign-in link.

### 8.2 Quota

Each member may have at most 5 *open* invitations, where open means `last_login IS NULL`. Completing a first sign-in frees a slot.

### 8.3 Duplicate handling

The `email` and `handle` columns are both unique. A collision produces a constraint error, and the interface reports "that address or that handle is not available" without distinguishing which — so the invite form cannot be used to enumerate members.

### 8.4 Bootstrap

The first member cannot invite themselves. At startup, if the `users` table is empty, the program reads a seed email and handle from flags or environment variables, creates the row with `invited_by` NULL, and prints a 24-hour sign-in link to standard output:

```
==================== ROOT LOGIN LINK ====================
member: root
valid:  24 hours
https://blog.example.com/l/xxxxxxxx
=========================================================
```

The markers make it findable in a container log stream. Once the table is non-empty, the seed values are ignored.

### 8.5 The social graph

Every member's inviter is displayed. In a closed community, knowing who vouched for whom is part of the trust structure. There is no way to hide it.

---

## 9. Posts

### 9.1 Structure

A post has a subject (one line, maximum 200 characters) and a body (plain text, maximum 16 KB). A reply has a body only (maximum 4 KB). There is no nesting; replies are a flat list.

### 9.2 Ownership

The creating member owns the post. Ownership grants the right to delete the post, which cascades to all its replies, and the right to delete any reply within it. A reply's author may delete their own reply. This is expressed in one statement:

```sql
DELETE FROM replies WHERE id = ? AND (
    user_id = ? OR post_id IN (SELECT id FROM posts WHERE user_id = ?)
)
```

Zero rows affected means the member had no right to delete it — indistinguishable from the row not existing, which is the correct behaviour.

### 9.3 The feed

`GET /feed` lists posts newest-first, 50 per page, with the subject, author handle, creation time, and reply count.

### 9.4 Editing

There is no edit function. The `updated_at` column exists in the schema for a future version but is currently always equal to `created_at`.

---

## 10. Chat

### 10.1 Structure

A channel is a post with `is_chat = 1`. Its name is the subject; its topic is the body and may be empty. Any member creates a channel by naming it — there is no approval step.

A chat message is a reply to that channel, maximum 2 KB.

### 10.2 The channel list

`GET /chat` lists channels sorted by `last_at` descending, so an active channel rises to the top. This differs from the blog feed, which sorts by creation time.

### 10.3 The retention limit

Each channel keeps the newest 500 messages (configurable). Older messages are deleted permanently and are not recoverable except from a volume snapshot.

The trim happens inside the same transaction as the insert:

```sql
-- 1. insert the message
INSERT INTO replies (post_id, user_id, body, created_at) VALUES (?, ?, ?, ?);

-- 2. move the channel to the top of the list
UPDATE posts SET last_at = ? WHERE id = ?;

-- 3. trim
DELETE FROM replies WHERE post_id = ? AND id <= (
    SELECT id FROM replies WHERE post_id = ?
    ORDER BY id DESC LIMIT 1 OFFSET ?
);
```

The inner `SELECT` with `OFFSET 500` returns the id of the 501st-newest message. The `DELETE` removes that row and every older one, leaving exactly 500. If the channel has fewer than 501 messages, the inner select returns no row, the comparison against NULL is never true, and nothing is deleted — the correct result with no separate count query.

The trim uses the row id rather than the timestamp, because two messages can share the same second.

**Three consequences:**

1. The limit is per channel, not per site. A member with 10 channels holds 10 × 500 messages.
2. Lowering the configured limit does not shrink a quiet channel; the trim only runs when a message arrives.
3. In the steady state, the trim costs one index seek and one row delete per message.

### 10.4 The channel page

`GET /c/{id}` renders the newest 100 messages, oldest first so the page reads top to bottom. There is no backwards pagination, because the retention limit already bounds the history.

Each message renders as one line:

```
09:22  geoff: Nice
09:23  geoff: Yeah
09:23  geoff: Word
```

This is a CSS grid with four columns — time, handle, text, delete control — so the columns align down the page and a wrapped message aligns under the text column rather than under the timestamp.

### 10.5 Live updates

New messages arrive without a page reload, via a poll:

```html
<div id="poller"
     hx-get="/c/{id}/lines?after={last_id}"
     hx-trigger="every 3s, newmsg from:body"
     hx-swap="outerHTML"></div>
```

The endpoint returns any messages with an id above `after`, followed by a replacement poller carrying the new highest id. Because the poller replaces itself with `outerHTML`, new messages land in the correct position and the poller returns to the end.

When the member sends a message, the POST returns `204 No Content` with the header `HX-Trigger: newmsg`. This fires the poller immediately, so the member's own message arrives through exactly the same path as everyone else's. **There is one render path and one insert path, so a duplicate message cannot appear.**

Both the channel page and the poll fragment render each message through the same `line.mustache` partial and the same `chatItem` context function in Go, so the two paths cannot diverge.

**Load characteristics.** Each open page makes one request every 3 seconds. Twenty readers produce 400 requests per minute against a single database connection, each one indexed query usually returning nothing.

**Known behaviour:** the poll only appends. Messages removed by the trim remain visible to a reader who has the page open until they reload.

---

## 11. Body rendering and linkification

*New in v1.1.*

### 11.1 The problem

Bodies are plain text and must be escaped, or a member could inject HTML. But bare URLs should be clickable. The obvious approach — building an `<a>` tag in Go and emitting it with Mustache's unescaped `{{{value}}}` form — is forbidden, because that form does no escaping and one mistake there is an injection.

### 11.2 The solution: segments

`Linkify` splits a body into a list of segments in Go. Each segment is either plain text or a link, never both:

```go
type Segment struct {
    Text   string   // display text
    URL    string   // empty unless IsLink
    IsLink bool
}
```

The template loops over the segments and uses `{{text}}` and `{{url}}` — both the escaping double-brace form. **The triple-brace form appears nowhere in this project.**

```html
{{#parts}}{{#is_link}}<a href="{{url}}" ...>{{text}}</a>{{/is_link}}{{^is_link}}{{text}}{{/is_link}}{{/parts}}
```

### 11.3 Scheme filtering

The scanner recognises only `http://` and `https://`. No other scheme can produce a link, so `javascript:` and `data:` can never reach an `href` attribute. A member who types `javascript:alert(1)` sees it rendered as escaped text.

### 11.4 Boundary rules

| Rule | Reason |
|---|---|
| A scheme must start the text or follow a break character | `xhttps://example.com` is not a link |
| Terminates at whitespace, `<`, `>`, `"`, `'`, `(`, `[`, `{`, `,` | Prevents swallowing surrounding markup or prose |
| Trailing `.`, `,`, `;`, `:`, `!`, `?` removed | Sentence punctuation is not part of the address |
| Something must follow `://` | A bare scheme is not an address |

### 11.5 Display shortening

A link longer than 60 characters is shortened for display: the scheme prefix is dropped, and the remainder is truncated with an ellipsis. **The `href` always carries the complete address.** This prevents one long URL from dominating a chat line or a paragraph.

### 11.6 Where it applies

| Field | Linkified |
|---|---|
| Post body | Yes |
| Blog reply body | Yes |
| Chat message body | Yes |
| Post subject / channel name | **No** |
| Channel topic | **No** |
| Handle, terms, footer | **No** |

Single-line fields are excluded deliberately: a link in a title would sit inside an anchor already, and the fields are short enough that a bare URL is readable.

### 11.7 Anchor attributes

```html
<a href="{{url}}" target="_blank" rel="noopener noreferrer" class="underline ...">
```

`rel="noopener noreferrer"` is set because `target="_blank"` otherwise gives the opened page a handle on the originating window. The `Referrer-Policy: same-origin` header already stops referrer leakage, but `noreferrer` does not depend on that header staying in place.

### 11.8 Template constraint

Bodies render inside a container with `whitespace-pre-wrap`. **The linkification section must therefore be written on a single line in the template**, because a newline inside the section becomes a visible line break in the rendered output.

The same constraint applies more severely to `line.mustache`, where the whole `<article>` is one line: a newline between grid children renders as a space and breaks column alignment.

---

## 12. Geoblocking

### 12.1 Purpose and honesty about it

Geoblocking removes unwanted traffic from specified countries. **It is best-effort and is not a security control.** VPN services, proxies, and stale IP allocation data make the determination wrong for some clients.

### 12.2 Determining the country

Two sources, in priority order:

1. **A trusted proxy header.** If the peer address falls within a configured trusted-proxy prefix, the `CF-IPCountry` header is used.
2. **A local range table.** A CSV file of `start_ip,end_ip,country_code` loaded at startup into a sorted array and searched by binary search.

If neither is available, no request is blocked and the program logs a warning at startup. The table covers IPv4 only.

### 12.3 Client address

Behind a proxy, the real client address comes from the first entry of `X-Forwarded-For`, but **only if the peer is in the trusted-proxy list**. Without this check, any client could spoof their apparent location.

This address is also what appears on the keys page.

### 12.4 Country codes

ISO 3166-1 alpha-2. The code for the United Kingdom is **GB**; `UK` is not an assigned code and blocks nothing. The configuration validator logs a warning if `UK` appears.

### 12.5 Placement

The geoblock is the outermost middleware. A blocked request receives a plain-text 403 under 100 bytes and touches nothing else. `/healthz` is exempt, because a platform health check from a blocked region would otherwise stop the container.

---

## 13. HTTP surface

### 13.1 Public routes

| Method | Path | Purpose |
|---|---|---|
| GET | `/healthz` | Health check; exempt from geoblock |
| GET | `/` | Sign-in form |
| POST | `/login` | Request a sign-in link |
| GET | `/l/{token}` | Consume the link, create a session |
| GET | `/terms` | Terms text |

### 13.2 Member routes

All require a valid session.

| Method | Path | Purpose |
|---|---|---|
| GET | `/feed` | Blog post list |
| GET | `/p/{id}` | One post with replies |
| POST | `/p` | Create a post |
| POST | `/p/{id}/delete` | Delete a post (owner only) |
| POST | `/p/{id}/r` | Add a reply |
| POST | `/r/{id}/delete` | Delete a reply |
| GET | `/chat` | Channel list |
| POST | `/chat` | Create a channel |
| GET | `/c/{id}` | One channel |
| GET | `/c/{id}/lines` | New messages fragment (HTMX) |
| POST | `/c/{id}/m` | Send a message |
| POST | `/c/{id}/delete` | Delete a channel (owner only) |
| GET | `/keys` | Session list |
| POST | `/keys/revoke-others` | Remove all other sessions |
| POST | `/logout` | Remove this session |
| GET | `/invite` | Invite form |
| POST | `/invite` | Create a member and send the link |
| GET | `/static/` | Client script |

### 13.3 Conventions

- Every state-changing route is POST and carries a CSRF token.
- Successful POSTs redirect (303 See Other), so a reload does not repeat the action. The exception is the chat message POST under HTMX, which returns 204.
- Requests with `HX-Request: true` receive fragments or empty bodies; the same routes without it receive redirects. **Every feature works with JavaScript disabled**, losing only the Enter-to-send shortcut and live updates.

---

## 14. Templates and styling

### 14.1 Template engine

Mustache, via `github.com/cbroglie/mustache`. Chosen for its logic-less design: templates cannot contain expressions, so display logic must live in Go.

All templates live in `code/template/` and are embedded with `go:embed`, parsed once at startup into a map. Each file is also registered as a partial.

At startup the program verifies that every required template is present and refuses to start if one is missing, naming the file. Without this check, a missing template produces a 500 on every request — and on the chat poll, several times per minute.

**Templates are embedded at build time.** Editing a `.mustache` file has no effect until the binary is rebuilt. This is the single most common source of confusion during development.

### 14.2 Escaping — critical

**Every value originating from a member uses the double-brace form `{{value}}`, which escapes HTML. The triple-brace form `{{{value}}}` appears nowhere in this project.**

Mustache escapes for HTML text context only. Therefore no member-supplied value may be placed in an `href`, `src`, inline style, or `<script>` block — with the single controlled exception of the linkification `href`, which is safe because the scheme filter in §11.3 guarantees the value begins with `http://` or `https://`.

### 14.3 Dotted names in sections

Mustache implementations vary in whether a dotted name works in a *section* tag as opposed to a *variable* tag. The project avoids the risk: context keys used as sections are flat top-level names (`post_parts`, not `post.parts`).

### 14.4 Newlines

Post, reply, and chat bodies are multi-line. No template or Go code converts newlines. The container carries `whitespace-pre-wrap`, so line breaks render correctly with zero processing and zero injection risk.

### 14.5 Rendering procedure

Templates render into a buffer first. Only on success are the status code and headers written. This prevents a partially-rendered page being served with a 200 status.

### 14.6 Styling: Tailwind from a CDN

*Changed in v1.1.* The hand-written style sheet has been removed. Styling is Tailwind's browser build, loaded from a CDN with a subresource-integrity hash, matching how HTMX is loaded.

```html
<script src="https://cdn.jsdelivr.net/npm/@tailwindcss/browser@4.1.11"
        integrity="sha384-..." crossorigin="anonymous"></script>
```

There is no build step, no Node.js, and no CLI. Utility classes live in the template markup. Theme values are set in an inline `<style type="text/tailwindcss">` block containing an `@theme` declaration.

**Consequence for the CSP.** The browser build generates styles at runtime and injects a `<style>` element, which requires `style-src 'unsafe-inline'`. This is a real loosening, accepted deliberately. The mitigating factors: Mustache escapes every member value, and no member value reaches an attribute except the scheme-filtered `href`.

**Consequence for page load.** The script must download and generate CSS before anything is styled, so a slow connection shows briefly unstyled HTML.

### 14.7 Design conventions

- Single narrow column, `max-w-3xl`, system fonts.
- Zinc scale for surfaces and text; blue for links and handles.
- Dark mode via Tailwind's `dark:` variant using the media strategy — no toggle, no cookie, no JavaScript.
- Focus rings on every interactive element.
- Mobile-first: the wide layout is the `sm:` variant.

### 14.8 Security headers

```
X-Content-Type-Options: nosniff
Referrer-Policy: same-origin
Content-Security-Policy: default-src 'none';
                         script-src 'self' https://cdn.jsdelivr.net;
                         connect-src 'self';
                         style-src 'self' 'unsafe-inline';
                         form-action 'self';
                         base-uri 'none'
```

`connect-src 'self'` is required because HTMX uses `XMLHttpRequest`, which is governed by `connect-src`, not `form-action`.

HTMX 2 injects an inline `<style>` element by default. It is disabled with:

```html
<meta name="htmx-config" content='{"includeIndicatorStyles":false}'>
```

This is retained even though `unsafe-inline` is now permitted, because it keeps the injected-style surface smaller.

---

## 15. Client-side behaviour

### 15.1 HTMX

Loaded from a CDN with an integrity hash. Used in exactly three places: the chat message form, the chat poller, and chat message deletion. Everything else is plain HTML forms.

### 15.2 chat.js

About 40 lines, served from `/static/`, never inline. Four functions:

**Enter sends, Shift+Enter inserts a newline.**

```js
box.addEventListener("keydown", function (evt) {
    if (evt.key !== "Enter" || evt.shiftKey || evt.isComposing) { return; }
    evt.preventDefault();
    if (box.value.trim() !== "") { form.requestSubmit(); }
});
```

The `isComposing` check is required: without it, an East Asian input method sends the message when Enter is pressed to accept a candidate word.

`requestSubmit()` fires a real submit event, which HTMX intercepts — so the request goes through `hx-post`, not a page navigation.

**Auto-growing input**, capped at six lines. This sets an inline `style` attribute, which is not governed by `style-src`.

**Clearing the box after a successful send**, via the `htmx:afterRequest` event.

**Scroll following.** After each swap, scroll to the newest message — but only if the reader was already at the bottom.

### 15.3 Chat layout

Four properties, each arrived at by fixing a specific defect.

**Full-height column.** `h-[calc(100dvh-8rem)]`. `dvh` is required; `vh` is wrong on mobile browsers with a retracting address bar.

**Bottom-anchored messages.** `mt-auto` on a wrapper inside the flex column. An auto margin absorbs free space, pushing a short message list down to the input box and collapsing to zero when the list is long.

**Do not use `justify-end`.** With content taller than the pane, several browsers place the top of the content above the scroll origin where it cannot be reached.

**Grid columns, not hanging indent.** `grid-cols-[3.5rem_6rem_minmax(0,1fr)_auto]`. An earlier version used `padding-left` with an equal negative `text-indent`; those are independent properties, and a later rule separated them, hiding the timestamp column entirely. A grid declares the columns once and cannot come apart.

`minmax(0,1fr)` and not `1fr` on the text column: a plain `1fr` has a content-based minimum, so one long unbroken word widens the column past the pane and produces a horizontal scrollbar.

**Hover-only delete control.** `invisible` plus `group-hover:visible`, with `group` on the article. A visible control on every line makes the page unreadable.

---

## 16. Security model

### 16.1 What is protected

| Asset | Protection |
|---|---|
| Member email addresses | Never rendered; only the handle is public |
| Session tokens | 32 random bytes, only the SHA-256 hash stored |
| Login tokens | Same, plus single-use and 15-minute expiry |
| Content | Every read requires a session |
| Membership list | Identical responses for known and unknown addresses |

### 16.2 CSRF

The token is derived, not stored:

```go
mac := hmac.New(sha256.New, app.secret)   // 32 random bytes, per process
mac.Write([]byte(sessionToken))
csrf := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
```

Every form carries it in a hidden field; every state-changing handler verifies it with `hmac.Equal`. Because it is derived from the session token, it needs no table and no server-side state, and it is automatically invalidated when the session ends.

The process secret is regenerated at each start, so a restart invalidates outstanding forms. Members see "the form is expired, try again".

`SameSite=Lax` on the cookie provides a second layer.

### 16.3 SQL injection

Every query uses parameter placeholders. The only string concatenation in a query is the sort direction in `ListPosts`, chosen from two local constants with no external input.

### 16.4 Input validation

All text passes through `CleanText`, which normalises line endings, strips control characters and invalid UTF-8, and trims whitespace. Single-line values additionally collapse runs of whitespace.

Length limits are enforced server-side. Request bodies are capped at 32 KB by `http.MaxBytesReader`.

Handles are restricted to letters, digits, `_`, `-`, and `.`.

### 16.5 Link injection

The linkification path is the only place where member input reaches an HTML attribute. Three independent layers protect it:

1. The scanner produces only `http://` and `https://` addresses.
2. Mustache escapes `{{url}}`, so a quote inside the address cannot break out of the attribute.
3. The CSP has `base-uri 'none'` and `form-action 'self'`.

### 16.6 Mail header injection

Header values passed to the SMTP layer have carriage returns and newlines stripped and are truncated to 200 characters.

### 16.7 Logging

The request log rewrites `/l/<token>` to `/l/[token]`. No email address, token, or password appears in any log line. Error responses contain no internal error text.

### 16.8 What is not protected

- **A sign-in link in an email is a bearer credential.** Anyone who reads the mailbox can sign in. This is inherent to the design; the keys page is the mitigation.
- **The geoblock is not a security control.**
- **There is no audit log.** Deletions leave no trace.
- **There is no rate limit on reads**, only on sign-in and content creation.
- **`style-src 'unsafe-inline'`** is permitted, as the cost of the CDN styling approach.
- **A member with root access to the host can read everything.** The database is not encrypted at rest by the application.

---

## 17. Configuration

### 17.1 Sources, in precedence order

1. Built-in defaults
2. `config.json`, if present (a missing file is not an error)
3. Environment variables
4. The `PORT` variable, which overrides the listen address

### 17.2 Settings

| Key | Environment | Default | Meaning |
|---|---|---|---|
| `site_name` | `BLOG_SITE_NAME` | Blog | Name in header and mail subject |
| `site_url` | `BLOG_SITE_URL` | *required* | Base URL for sign-in links |
| `listen` | `BLOG_LISTEN` | 127.0.0.1:8080 | Listen address |
| `db_path` | `BLOG_DB_PATH` | blog.db | Database file |
| `terms` | `BLOG_TERMS` | empty | Terms page text |
| `footer` | `BLOG_FOOTER` | empty | Footer text |
| `blocked_countries` | `BLOG_BLOCKED` | GB,AU | Geoblock list |
| `trusted_proxies` | `BLOG_TRUSTED_PROXIES` | empty | CIDR prefixes |
| `geo_v4_file` | — | empty | IP range CSV path |
| `smtp_host` | `BLOG_SMTP_HOST` | localhost:25 | Mail relay |
| `smtp_user` | `BLOG_SMTP_USER` | empty | Relay username |
| `smtp_pass` | `BLOG_SMTP_PASS` | empty | Relay password |
| `mail_from` | `BLOG_MAIL_FROM` | *required* | Sender address |
| `invite_quota` | `BLOG_INVITE_QUOTA` | 5 | Open invites per member |
| `session_days` | — | 30 | Session lifetime |
| `posts_per_page` | — | 50 | Feed page size |
| `chat_keep` | `BLOG_CHAT_KEEP` | 500 | Messages kept per channel |
| `chat_per_page` | — | 100 | Messages shown per channel |

Unknown keys in `config.json` are an error, so a typo is reported rather than silently ignored.

### 17.3 Reloading

`SIGHUP` re-reads the configuration file and rebuilds the geo table. `listen` and `db_path` are not reloadable. If a reload fails, the previous configuration stays active and the error is logged.

### 17.4 Mail

Port 25 is blocked outbound on essentially every cloud network, so a relay on port 587 with credentials is required in practice. The client uses STARTTLS when offered, and `smtp.PlainAuth` refuses to send credentials over an unencrypted connection — so the STARTTLS step must and does come first.

Mail is sent in a goroutine with a 10-second deadline so a slow relay never delays an HTTP response.

---

## 18. Source layout

*Changed in v1.1.* All Go source, templates, and static assets live under `code/`. Templates are in `code/template/`.

```
blogchat/
  code/
    app.go              App struct, domain types, context helpers
    config.go           Config, loading, validation, env, reload
    db.go               Schema, migrations, every query
    token.go            Token generation and hashing
    auth.go             Sign-in flow, sessions, cookies, CSRF
    limit.go            Rate limiter, last-seen cache
    validate.go         Input cleaning and validation
    linkify.go          URL scanning and segmentation
    handlers.go         All HTTP handlers
    routes.go           Router, static files, request log
    views.go            Template loading and rendering
    geo.go              Country lookup, geoblock middleware
    mail.go             SMTP client
    main.go             Startup, signals, shutdown
    template/           15 mustache files
    static/
      chat.js
  terraform/            Four platform configurations
  Dockerfile
  config.json
  README.md
```

Flat within `code/`. This is appropriate for a single-binary program of this size.

### 18.1 Embed paths

Two directives, both relative to `code/`:

```go
//go:embed template/*.mustache    // views.go
//go:embed static                 // routes.go
```

A rename of either directory requires editing the directive, the `fs.ReadDir` call, and the `ReadFile` prefix. A mismatch produces a build failure reading `pattern ...: no matching files found`, not a runtime error.

### 18.2 Naming convention

No identifier is shorter than three characters. Receivers are `app`, response writers `res`, requests `req`, users `usr`, transactions `txn`. The sole exception is the exported struct field `ID`, following Go's standard initialism convention.

### 18.3 Dependencies

Two Go modules, both pure Go:

- `modernc.org/sqlite` — SQLite driver, no cgo, so builds are static with no C toolchain
- `github.com/cbroglie/mustache` — templates

Everything else is the standard library. HTMX and Tailwind are loaded in the browser and are not build dependencies.

**Note on build time.** `modernc.org/sqlite` is a large transpiled package. A cold compile takes several minutes with no output. Subsequent builds use the cache and are fast. This surprises new contributors, who may mistake it for a hang.

---

## 19. Runtime behaviour

### 19.1 Startup

1. Load configuration; abort on error
2. Generate the 32-byte CSRF secret
3. Load the geo table
4. Open the database, creating the file if absent; run migrations
5. Load and verify templates
6. Seed the root member if the users table is empty
7. Start the hourly purge loop
8. Start the HTTP server

Any failure aborts with a message naming the cause.

### 19.2 Database connection

```
file:blog.db?_pragma=journal_mode(WAL)
            &_pragma=busy_timeout(5000)
            &_pragma=foreign_keys(1)
            &_pragma=synchronous(NORMAL)
```

- **WAL** allows concurrent readers during a write.
- **foreign_keys(1)** is required; SQLite disables them by default, and the cascade deletes depend on them.
- **synchronous(NORMAL)** does not flush the WAL on every commit. A clean shutdown or process crash loses nothing; a host power loss can lose the last few seconds.

`SetMaxOpenConns(1)` is what makes `SQLITE_BUSY` impossible.

### 19.3 Signals

| Signal | Effect |
|---|---|
| SIGHUP | Reload config and geo table |
| SIGTERM, SIGINT | Graceful shutdown |

Shutdown: stop accepting connections, allow 20 seconds for in-flight requests, run `PRAGMA wal_checkpoint(TRUNCATE)`, close the database. The platform's stop grace period must exceed 20 seconds; many default to 5.

### 19.4 Background work

- **Hourly:** delete expired sessions and login tokens older than a day.
- **Every 5 minutes:** clean the rate-limit map.
- **Every 30 minutes:** clean the last-seen cache.

### 19.5 Resource usage

Memory: a few megabytes, plus the geo table if loaded (about 2.5 MB for 250,000 IPv4 ranges). Binary: about 15 MB. Database: a few megabytes for a small community.

---

## 20. Deployment

### 20.1 Container image

Two-stage build. Stage one compiles with `CGO_ENABLED=0` and `-ldflags="-s -w"`. Stage two is `gcr.io/distroless/static-debian12:nonroot`, which supplies the CA bundle needed for STARTTLS and the timezone database, and contains no shell or package manager.

`/data` is the only writable path. Built for `linux/amd64` and `linux/arm64`.

### 20.2 Runtime arrangement

Two containers via Docker Compose: blogchat, and Caddy for TLS. The entire Caddy configuration:

```
blog.example.com {
    reverse_proxy blog:8080
}
```

**Why not a cloud load balancer:** a managed load balancer costs more per month than the instance running the whole system.

### 20.3 The storage constraint

| Storage type | Suitable | Reason |
|---|---|---|
| Block volume / disk | **Yes** | Correct POSIX locking |
| Instance local disk | Yes | Correct, but lost on instance rebuild |
| NFS / EFS | **No** | Advisory locks fail in ways SQLite cannot detect — corruption |
| Object storage via FUSE | **No** | No locking, no atomic rename |
| Ephemeral container FS | **No** | Data lost on every restart |

This rules out AWS App Runner, Google Cloud Run, and ECS on Fargate with EFS. The correct target is a small virtual machine with an attached block volume.

### 20.4 Terraform

Configurations for Google Cloud Platform, Amazon Web Services, DigitalOcean, and Vultr. Each creates: one VM, one 10 GB data volume, one static address, one firewall, and a daily snapshot schedule.

A shared cloud-init template does the same work on all four: locate the data disk, format it only if it has no filesystem, mount at `/data`, add an fstab entry, install Docker, write the compose file, start the containers.

**Docker must wait for the mount.** A systemd drop-in adds `RequiresMountsFor=/data`. Without it, Docker may start first, the container binds an unmounted directory on the boot disk, and a second empty database appears that vanishes at the next reboot.

**The format step must be conditional.** Formatting on every boot would destroy the database.

**The data volume has `prevent_destroy = true` with no override.** `terraform destroy` removes everything else and stops at the volume. This is the single most important safety property of the deployment.

### 20.5 Snapshots

One snapshot daily at 03:00 UTC, retained 28 days. GCP and AWS have native scheduled-snapshot resources; DigitalOcean and Vultr do not, so a cron job on the instance calls the provider API.

Snapshots are crash-consistent rather than clean. SQLite recovers from that state automatically.

**Restore:** create a disk from the snapshot, stop the instance, detach the current disk, attach the new one with the same device name, start. The mount script finds the `blogdata` label and proceeds normally.

---

## 21. Operations

### 21.1 First run

```bash
cd code && go build -o blog .
./blog -config ../config.json -seed-email you@example.com -seed-handle root
```

The root sign-in link is printed to standard output, valid 24 hours.

### 21.2 Development cycle

```bash
cd code && go build -o blog . && ./blog
```

**Templates and the client script are embedded at build time.** Editing a `.mustache` file or `chat.js` requires a rebuild and restart. This is the most common source of "my change did nothing".

### 21.3 Disabling a member

No admin interface exists. Use SQLite directly:

```sql
UPDATE users SET enabled = 0 WHERE handle = 'name';
```

Effective on the member's next request. **Do not delete the row** — the cascades destroy all their posts and messages, including replies written by others in their threads.

### 21.4 Updating

Change the image tag and redeploy. The instance is replaced; the volume is not. Always use a version tag; never `latest`.

### 21.5 Diagnosis

```bash
docker compose logs -f blog     # application
docker compose logs -f caddy    # certificate problems
curl localhost:8080/healthz     # bypasses proxy and firewall
ls -la /data/blog.db*           # three files: db, -wal, -shm
df -h /data                     # must be the data volume, not the boot disk
```

For rendering problems, view the raw page source rather than the inspector. The inspector shows the DOM after Tailwind and HTMX have run; the source shows what the server actually sent.

---

## 22. Limits and constants

### Content

| Item | Limit |
|---|---|
| Subject / channel name | 200 characters |
| Channel topic | 200 characters |
| Post body | 16 KB |
| Reply body | 4 KB |
| Chat message | 2 KB |
| Handle | 2–24 characters |
| Email address | 254 characters |
| Request body | 32 KB |
| Link display text | 60 characters |

### Time

| Item | Value |
|---|---|
| Sign-in link | 15 minutes, single use |
| Invitation link | 7 days, single use |
| Root bootstrap link | 24 hours, single use |
| Session | 30 days |
| Last-seen write interval | 60 seconds |
| Chat poll interval | 3 seconds |

### Rate limits (per member per minute unless noted)

| Action | Limit |
|---|---|
| Sign-in mails | 5 per 15 min, per address and per IP |
| Link opens | 20 per 15 min per IP |
| New posts | 10 |
| New replies | 20 |
| New channels | 5 |
| Chat messages | 60 |
| Open invitations | 5 total per member |

### Pagination

| Item | Value |
|---|---|
| Feed / channel list | 50 per page |
| Chat messages shown | 100 |
| Chat messages retained | 500 per channel |

---

## 23. Known limitations

**Cannot scale horizontally.** SQLite with one connection means one instance, permanently.

**No editing.** Posts, replies, and messages cannot be changed after submission.

**No search.** No full-text search over posts or messages.

**No notifications.** Members must visit to see new content.

**No file uploads.** Text only.

**No password recovery flow** — because there are no passwords. Access depends entirely on the email address remaining reachable.

**Handles are permanent.**

**No admin interface.** Member management requires direct database access.

**The inviter is always visible.**

**Chat history is lost by design.** Beyond 500 messages per channel, gone permanently except from volume snapshots.

**The geoblock is trivially bypassed** with any VPN, and covers IPv4 only.

**Two CDN dependencies.** HTMX and Tailwind. Members behind restrictive networks lose live chat updates and all styling, and each page load contacts a third party. Self-hosting either is possible at the cost of the integrity guarantee.

**`style-src 'unsafe-inline'` is required** by the Tailwind browser build.

**Flash of unstyled content.** Tailwind generates CSS at runtime, so a slow connection shows unstyled HTML briefly.

**Linkification recognises only `http` and `https`.** Bare domains without a scheme, `mailto:`, and other schemes are not linked. A URL containing a closing bracket that was opened outside the URL may be truncated.

**Deletion is unlogged.**

**The Vultr Terraform configuration is untested** and has a known circular dependency in the snapshot script.

---

## 24. Glossary

**Channel** — A chat room. Internally a `posts` row with `is_chat = 1`.

**CSRF** — Cross-site request forgery. Prevented here by a derived token in every form.

**Handle** — A member's public display name. Permanent.

**HTMX** — A browser library that adds AJAX behaviour through HTML attributes.

**Key** — One session, one device. Members can view and revoke them.

**Linkify** — The Go function that splits a body into text and link segments so that URLs become clickable without unescaped template output.

**Mustache** — A logic-less template language.

**Root member** — The first account, created at startup, with no inviter.

**Segment** — One part of a rendered body: plain text or a link, never both.

**Sign-in link** — A one-time URL that creates a session, sent by email.

**Tailwind** — A utility-class CSS framework, here loaded as a browser build from a CDN with no build step.

**Trim** — The deletion of chat messages beyond the retention limit, performed inside the insert transaction.

**WAL** — Write-Ahead Log. A SQLite journal mode allowing readers to proceed during a write.

---

*End of specification.*

---

# blogchat — Technical Specification Appendices

**Supplement to Version 1.1**

Reference material supporting the specification: complete signatures, decision records, failure catalogues, and verification procedures. Nothing here repeats the main document; each appendix cites the section it extends.

---

## Contents

- [A. Function reference](#appendix-a--function-reference)
- [B. Template context reference](#appendix-b--template-context-reference)
- [C. Decision record](#appendix-c--decision-record)
- [D. Rejected alternatives](#appendix-d--rejected-alternatives)
- [E. Failure catalogue](#appendix-e--failure-catalogue)
- [F. Query plans and index usage](#appendix-f--query-plans-and-index-usage)
- [G. State transitions](#appendix-g--state-transitions)
- [H. Verification matrix](#appendix-h--verification-matrix)
- [I. HTTP status codes in use](#appendix-i--http-status-codes-in-use)
- [J. Linkification test vectors](#appendix-j--linkification-test-vectors)
- [K. Tailwind class inventory](#appendix-k--tailwind-class-inventory)
- [L. Embed path and build-time coupling](#appendix-l--embed-path-and-build-time-coupling)
- [M. Extension points](#appendix-m--extension-points)
- [N. Cost model](#appendix-n--cost-model)
- [O. Dependency and version matrix](#appendix-o--dependency-and-version-matrix)
- [P. Threat table](#appendix-p--threat-table)
- [Q. Data lifecycle](#appendix-q--data-lifecycle)
- [R. Error message inventory](#appendix-r--error-message-inventory)
- [S. Migration record, v1.0 to v1.1](#appendix-s--migration-record-v10-to-v11)

---

## Appendix A — Function reference

Extends specification §18. Every function crossing a file boundary. Paths are relative to `code/`.

### A.1 Type definitions (`app.go`)

| Symbol | Kind | Notes |
|---|---|---|
| `App` | struct | All process state; every field concurrency-safe |
| `User` | struct | `InviterHandle` filled by LEFT JOIN, empty for root |
| `Session` | struct | `Current` set by the caller, not the query |
| `Post` | struct | Serves both blog posts and channels |
| `Reply` | struct | Serves both blog replies and chat messages |
| `FeedRow` | struct | Denormalised list row; avoids fetching bodies |
| `AuthHandler` | func type | `func(res, req, usr *User, raw string)` |
| `contextKey` | string type | Prevents collision with other packages' context keys |

The `raw` parameter carries the session token for CSRF verification. It deliberately does **not** carry the session row id, which is why three handlers call `SessionFrom` a second time — see D.7.

### A.2 Configuration (`config.go`)

| Function | Returns | Notes |
|---|---|---|
| `LoadConfig(path)` | `*Config, error` | Missing file is not an error; parse failure is |
| `(*Config) Validate()` | `error` | Also normalises: trims trailing slash, upper-cases country codes |
| `(*Config) applyEnv()` | — | Runs after file, before validation |
| `(*App) Reload(path)` | `error` | Preserves `Listen` and `DBPath` from startup |
| `setStr`, `setInt`, `setList` | — | Empty string means "not set"; a genuine empty value cannot be set by env |

### A.3 Tokens (`token.go`)

| Function | Returns | Notes |
|---|---|---|
| `NewToken()` | `(raw string, sum []byte)` | Panics if `crypto/rand` fails |
| `HashToken(raw)` | `(sum []byte, valid bool)` | `valid` false on bad base64 or wrong length |

### A.4 Linkification (`linkify.go`)

*New in v1.1.* Extends §11.

| Function | Returns | Notes |
|---|---|---|
| `Linkify(body)` | `[]Segment` | Top-level scanner |
| `findScheme(text)` | `int` | Offset of next `http`/`https`, or −1 |
| `isBreak(chr)` | `bool` | Characters that bound an address |
| `hasHost(link)` | `bool` | Something must follow `://` |
| `trimTrailing(link)` | `string` | Removes sentence punctuation; balances brackets |
| `shortenLink(link)` | `string` | Display text, capped at `maxLinkText` |
| `appendText(list, text)` | `[]Segment` | Merges adjacent text segments |
| `bodyParts(body)` | `[]map[string]any` | Template context form |

**This file imports only `strings` and `unicode`.** That is deliberate and useful: it can be copied into an empty module and tested in under a second, bypassing the multi-minute cold compile of the SQLite driver. See E.1.

`bodyParts` is the only function called from outside; the rest are internal to the scan.

### A.5 Database (`db.go`)

**Lifecycle**

| Function | Notes |
|---|---|
| `OpenDB(path)` | Creates file if absent, applies migrations |
| `Migrate(dbh)` | Each migration in its own transaction |
| `Checkpoint(dbh)` | `wal_checkpoint(TRUNCATE)`; called at shutdown |
| `boolInt(value)` | SQLite has no boolean type |

**Users**

| Function | Notes |
|---|---|
| `FindUserByEmail(email)` | Returns `ErrNotFound`, not `sql.ErrNoRows` |
| `FindUserByID(uid)` | Currently unused; retained for future admin work |
| `CountUsers()` | Bootstrap test |
| `CreateUser(email, handle, inviter)` | `inviter = 0` means root |
| `CountOpenInvites(uid)` | Counts `last_login IS NULL` |

**Tokens and sessions**

| Function | Notes |
|---|---|
| `CreateLoginToken(uid, sum, life)` | |
| `ConsumeLoginToken(sum)` | Transactional; state check inside the UPDATE |
| `CreateSession(uid, sum, addr, agent, life)` | Truncates agent to 120 chars |
| `SessionByHash(sum)` | Joins users; enforces `enabled = 1` |
| `ListSessions(uid, current)` | Sets `Current` on the matching row |
| `DeleteOtherSessions(uid, keep)` | Returns count for the notice message |
| `DeleteSession(uid, sid)` | `uid` prevents cross-member deletion |
| `TouchSession(sid)` | Called only when `SeenCache` permits |
| `PurgeExpired()` | Hourly; tokens kept an extra day for diagnosis |

**Posts and replies**

| Function | Filters `is_chat`? | Notes |
|---|---|---|
| `CreatePost(uid, subject, body, chat)` | sets it | Sets `last_at = now` |
| `ListPosts(chat, limit, offset)` | **yes** | Sort column depends on `chat` |
| `CountPosts(chat)` | **yes** | |
| `GetPost(pid)` | **no — deliberate** | Caller inspects `IsChat` to redirect |
| `DeletePost(pid, uid)` | no | Ownership is the only check needed |
| `CreateReply(pid, uid, body)` | no | Blog only; does not touch `last_at` |
| `ListReplies(pid)` | no | Blog only; unbounded |
| `ListChatLines(pid, limit)` | no | Newest N, returned oldest-first |
| `ListChatLinesAfter(pid, after, limit)` | no | Poll endpoint |
| `CreateChatLine(pid, uid, body, keep)` | no | Insert + `last_at` + trim, one transaction |
| `DeleteReply(rid, uid)` | no | Author or thread owner |

### A.6 Authentication (`auth.go`)

| Function | Notes |
|---|---|
| `SetSessionCookie(res, raw)` | `Secure` only when site URL is HTTPS |
| `ClearSessionCookie(res)` | `MaxAge: -1` |
| `SessionFrom(req)` | `(usr, sid, raw, found)` |
| `RequireAuth(hnd)` | Wraps; injects CSRF into context; updates last-seen after |
| `CSRFFor(raw)` | HMAC-SHA256 of session token under process secret |
| `CheckCSRF(req, raw)` | `hmac.Equal` |
| `StartLogin(email)` | **Returns nothing** — response must not reveal outcome |
| `FinishLogin(res, req, token)` | Consumes token, creates session, sets cookie |

### A.7 Validation (`validate.go`)

| Function | Notes |
|---|---|
| `CleanText(text)` | Preserves `\n` and `\t`; strips other control chars and `RuneError` |
| `CleanLine(text)` | `CleanText` then collapses whitespace runs |
| `ValidSubject(text)` | Non-empty, ≤200 runes |
| `ValidBody(text, limit)` | Non-empty, ≤limit **bytes** |
| `ValidTopic(text)` | **May be empty**, ≤200 runes |
| `ValidEmail(text)` | Minimal structural check only |
| `ValidHandle(text)` | 2–24 runes, restricted character set |

Note the deliberate inconsistency: single-line limits count runes (display width matters); body limits count bytes (storage size matters).

### A.8 Handlers, rendering, geo, mail, routing

| Function | File | Notes |
|---|---|---|
| `timeText(unix)` | handlers.go | `2006-01-02 15:04` |
| `clockText(unix)` | handlers.go | `15:04` — chat lines only |
| `isHTMX(req)` | handlers.go | Tests `HX-Request: true` |
| `chatItem(line, canDelete)` | handlers.go | **Single source of chat line context** — used by page and fragment |
| `parseForm(res, req)` | handlers.go | Applies `MaxBytesReader` |
| `redirectError(res, req, target, msg)` | handlers.go | 303 with message in query |
| `urlValue(text)` | handlers.go | Hand-rolled query encoder |
| `LoadViews()` | views.go | Parses all; verifies required list; fails startup if any missing |
| `(*App) Base(req, usr)` | views.go | Reads CSRF from request context |
| `(*App) Render(...)` | views.go | Buffers first; sets security headers |
| `(*App) Fail(...)` | views.go | Falls back to plain text if `error` template broken |
| `LoadGeo(conf)` | geo.go | Warns if block list set but no source available |
| `(*Geo) Lookup(addr)` | geo.go | IPv4 only; binary search |
| `(*Geo) ClientAddr(req)` | geo.go | XFF only from trusted peer |
| `(*Geo) Country(req)` | geo.go | Header beats table |
| `(*App) GeoBlock(next)` | geo.go | Exempts `/healthz` |
| `(*App) SendMail(to, subj, body)` | mail.go | STARTTLS then optional auth |
| `(*App) Routes()` | routes.go | Returns handler wrapped in request log |

`chatItem` is the reason chat linkification was a two-line change: both render paths call it.

---

## Appendix B — Template context reference

Extends specification §14.

### B.1 Base keys — present in every context

| Key | Type | Source |
|---|---|---|
| `site_name` | string | Config |
| `footer` | string | Config |
| `terms` | string | Config |
| `handle` | string | Current user, empty if none |
| `authed` | bool | Whether a session exists |
| `csrf` | string | Request context, set by `RequireAuth` |

### B.2 Page templates

| Template | Additional keys |
|---|---|
| `login` | `sent`, `error` |
| `feed` | `posts[]`, `empty`, `chat`, `base`, `page`, `has_prev`, `prev_page`, `has_next`, `next_page`, `error` |
| `chats` | as `feed`, plus `keep` |
| `post` | `post{}`, `post_parts[]`, `replies[]`, `reply_count`, `owner`, `error` |
| `chat` | `post{}`, `channel_id`, `has_topic`, `lines[]`, `line_count`, `empty`, `owner`, `keep`, `last_id`, `error` |
| `lines` | `channel_id`, `lines[]`, `last_id` — **fragment; only `csrf` from base** |
| `keys` | `sessions[]`, `others`, `has_others`, `inviter`, `joined`, `notice` |
| `invite` | `sent`, `error`, `remaining`, `can_invite` |
| `terms` | none |
| `error` | `code`, `msg` |
| `blocked` | `country` — **unused; middleware sends plain text** |

`post_parts` is a top-level key rather than `post.parts`. See B.5.

### B.3 Partials

| Partial | Used by | Required keys |
|---|---|---|
| `header` | all pages | base keys |
| `footer` | all except `chat` | `footer` |
| `nav` | `post`, `terms` | none |
| `postform` | `feed` | `csrf` |
| `replyform` | `post` | `csrf`, `post.id` |
| `channelform` | `chats` | `csrf` |
| `messageform` | `chat` | `csrf`, `post.id` |
| `line` | `chat`, `lines` | `id`, `when`, `handle`, `parts[]`, `can_delete`, `csrf` |

`chat` omits the footer partial deliberately: a footer inside a full-height flex layout consumes vertical space on every screen. The template closes `</body></html>` itself.

### B.4 Item shapes

**`posts[]`** — `id`, `subject`, `handle`, `when`, `replies`. `when` is creation time for blog, `last_at` for channels.

**`replies[]`** — `id`, `handle`, `parts[]`, `when`, `can_delete`. *Changed in v1.1: `body` replaced by `parts`.*

**`lines[]`** — same shape, `when` is `HH:MM`. *Changed in v1.1.*

**`parts[]`** — `text`, `url`, `is_link`. Produced by `bodyParts`.

**`sessions[]`** — `id`, `when`, `last`, `ip`, `agent`, `current`.

**`post{}`** — `id`, `subject`, `handle`, `when`. *Changed in v1.1: `body` moved out to top-level `post_parts`.*

### B.5 Dotted names: variable versus section

`{{post.id}}` in a variable tag works. `{{#post.parts}}` in a **section** tag is not supported by every Mustache implementation, and a silently empty section is the failure mode — the body simply renders as nothing, with no error anywhere.

The project therefore uses flat top-level keys for anything iterated as a section. This is why the post body context is `post_parts` and not `post.parts`.

`line.mustache` does still rely on parent-context resolution for `{{post.id}}` inside `{{#lines}}` — a variable tag, which is safe.

### B.6 Single-line template constraint

Two templates must keep specific content on one physical line:

| Template | What | Why |
|---|---|---|
| `line.mustache` | The entire `<article>` | A newline between grid children renders as a space and breaks column alignment |
| `post.mustache` | Each `{{#parts}}...{{/parts}}` block | The container has `whitespace-pre-wrap`; a newline becomes a visible line break in the body |

This is the most common source of visual defects when editing templates by hand.

---

## Appendix C — Decision record

Extends specification §2. What was decided, why, and what it costs.

### C.1 Storage and structure

| # | Decision | Rationale | Cost |
|---|---|---|---|
| 1 | SQLite over Postgres | No server to operate; backup is one file | Cannot scale beyond one instance |
| 2 | One connection (`MaxOpenConns(1)`) | Eliminates `SQLITE_BUSY` entirely | All DB access serialised |
| 3 | `synchronous(NORMAL)` | Avoids fsync per commit | Host power loss can lose seconds |
| 4 | Shared `posts`/`replies` tables | One insert path, one ownership rule, one index set | Every post query must filter `is_chat` |
| 5 | `last_at` as a stored column | Index-scan sort for the channel list | Must be updated on every message |
| 6 | Trim by row id, not timestamp | Two messages can share a second | None |
| 7 | Trim inside the insert transaction | Count cannot drift | Slightly longer write transaction |
| 8 | Source under `code/` | Separates program from `terraform/`, Dockerfile, docs | Embed paths are relative to it |

### C.2 Authentication

| # | Decision | Rationale | Cost |
|---|---|---|---|
| 9 | No passwords | No credential store to protect | Access depends on mailbox reachability |
| 10 | Store token hashes only | DB leak yields no usable credentials | None |
| 11 | Identical response for unknown addresses | Prevents membership enumeration | User cannot tell if they mistyped |
| 12 | Redirect to `/keys` after sign-in | Makes unrecognised sessions visible immediately | One extra page in the flow |
| 13 | Derived CSRF token | No table, no server state, auto-invalidated | Restart expires open forms |
| 14 | Process secret regenerated at start | No secret to manage or rotate | Forms expire on restart |
| 15 | Invite creates the account immediately | No pending-invite table, no redeem step | Handle chosen by inviter, not invitee |
| 16 | Quota counts `last_login IS NULL` | Self-clearing; no expiry job | Never-signing-in invitee holds a slot forever |

### C.3 Rendering and interface

| # | Decision | Rationale | Cost |
|---|---|---|---|
| 17 | Mustache over `html/template` | Logic-less; forces display logic into Go | Loses context-aware escaping — see P.3 |
| 18 | `whitespace-pre-wrap` for newlines | Zero processing, zero injection risk | Constrains template line breaks — B.6 |
| 19 | Buffer before writing response | No partial page with 200 status | Small memory cost per request |
| 20 | Startup check for required templates | Missing template would 500 on every poll | Must maintain the required list |
| 21 | HTMX over hand-written JS | ~40 lines of JS total | CDN dependency |
| 22 | Poll, not SSE | Compatible with one DB connection and 15s write timeout | 3s latency; steady request load |
| 23 | POST returns 204 + `HX-Trigger` | One render path; duplicates impossible | Message appears via poll, not response |
| 24 | CSS grid for chat lines | Columns declared once, cannot desynchronise | Replaced a defective hanging-indent — E.4 |
| 25 | `mt-auto` for bottom-anchoring | Correct in all browsers | Requires a wrapper element — D.8 |
| 26 | Both `method`/`action` and `hx-*` on forms | Works without JavaScript | Handlers branch on `HX-Request` |
| 27 | Flat context keys for sections | Dotted section names are not portable | Slightly less tidy context shape |

### C.4 Linkification (new in v1.1)

| # | Decision | Rationale | Cost |
|---|---|---|---|
| 28 | Segments in Go, not HTML in the template | Triple-brace stays banned; escaping unchanged | Template gains a loop |
| 29 | `http` and `https` only | `javascript:` and `data:` cannot reach an href | Bare domains and `mailto:` are not linked |
| 30 | Display shortening at 60 chars | One URL cannot dominate a chat line | Displayed text differs from the href |
| 31 | Bodies only, not titles | A title link inside a heading anchor is redundant | Inconsistent at first glance |
| 32 | `linkify.go` imports nothing beyond stdlib | Can be tested in an empty module in one second | None |
| 33 | Shared cap for blog and chat | One constant, one function | Chat may prefer a shorter cap — D.13 |

### C.5 Styling (changed in v1.1)

| # | Decision | Rationale | Cost |
|---|---|---|---|
| 34 | Tailwind browser build from CDN | No Node, no CLI, no build step — matches HTMX | Requires `style-src 'unsafe-inline'` |
| 35 | Hand-written CSS deleted entirely | One styling system, not two | Brief flash of unstyled content |
| 36 | Dark mode via media strategy | No toggle, no cookie, no JavaScript | Cannot be overridden per member |
| 37 | Zinc + one accent | Sufficient for a text-only interface | — |
| 38 | Mobile-first (`sm:` for wide) | Tailwind's native direction | Inverts the old media query |
| 39 | Retain `includeIndicatorStyles:false` | Keeps injected-style surface small | None |

### C.6 Deployment

| # | Decision | Rationale | Cost |
|---|---|---|---|
| 40 | Caddy on the instance, not a cloud LB | LB costs more than the whole system | TLS terminates on the instance |
| 41 | `prevent_destroy` with no override | The volume is the entire system's value | Teardown is deliberately incomplete |
| 42 | Snapshots only, no file backup | Crash-consistent is sufficient at this write rate | No provider-independent restore path |
| 43 | Fixed snapshot schedule | Two fewer variables to explain | Editing the module is required to change |
| 44 | Distroless image | No shell, no package manager | DB inspection needs a second container |
| 45 | ARM on AWS | Cheaper; image already multi-arch | Requires `arm64` in the build |
| 46 | Public image, no registry auth | Removes credential handling from cloud-init | Image contents are public |

---

## Appendix D — Rejected alternatives

Extends Appendix C. Options considered and declined.

### D.1 `internal/` + `cmd/` source layout

Implemented, then reverted. Requires `cmd/blog/main.go` plus `internal/blog/*.go`, moving templates under `internal/blog/` because `go:embed` cannot reference a parent directory.

**Rejected because:** at this size it adds two directory levels and an exported/unexported boundary serving no consumer. The `code/` directory (C.8) achieves the separation that mattered — program apart from infrastructure — with one level.

### D.2 Postgres or MySQL

**Rejected because:** introduces a server process, credentials, a separate backup procedure, and a network dependency — in exchange for horizontal scaling this system explicitly does not want.

### D.3 Server-Sent Events for chat

**Rejected because:** a long-lived connection per open page conflicts with the 15-second write timeout and holds a goroutine per reader.

### D.4 `<meta http-equiv="refresh">` for chat updates

**Rejected because:** it discards text in the input box on every refresh.

### D.5 Rotate-180 chat pane

Rotating the scroll container 180° and each message back anchors content at the bottom by construction.

**Rejected because:** requires reversing the query order, breaks text selection across messages in some browsers, and inverts scroll-wheel direction unless separately corrected.

### D.6 Tailwind CLI at build time

The standalone CLI (no Node required) scans templates and emits a static CSS file, which could be embedded like any other asset. This would keep `style-src 'self'` and eliminate the flash of unstyled content.

**Rejected by project direction.** Recorded because the trade is real: the CLI costs a build step and gains a stricter CSP.

### D.7 Passing the session id through `AuthHandler`

Three handlers call `SessionFrom` twice because `AuthHandler` carries only the user and raw token.

**Rejected because:** the signature was frozen before the handlers existed, and the second call is one indexed lookup on a local file.

### D.8 `justify-end` for bottom-anchored chat

**Rejected because it is defective:** when content exceeds the container height, several browsers place the top of the content above the scroll origin. `mt-auto` has no such behaviour. The Tailwind class name makes this mistake more tempting, not less.

### D.9 Cloud Run, App Runner, Fargate+EFS

**Rejected because:** none provides storage with correct POSIX locking. Using any of them would corrupt the database — not fail loudly, corrupt.

### D.10 Cloud load balancer with managed certificate

**Rejected because:** roughly $16–18/month, more than the instance running the entire system.

### D.11 `VACUUM INTO` file backup alongside snapshots

**Rejected as over-engineering** for the stated write rate. Retained as a recommendation for deployments wanting provider-independent restore.

### D.12 SIGUSR1 checkpoint before snapshot

**Rejected** for the same reason as D.11: coordination between scheduler and process to remove a negligible risk.

### D.13 Separate link-length caps for blog and chat

Chat lines are visually shorter than blog paragraphs, so a 60-character link is proportionally more intrusive there.

**Deferred.** Would require a length parameter on `bodyParts` and a second constant. One constant is currently shared. Change if chat readability suffers in practice.

### D.14 Markdown or any member markup

**Rejected because:** it reintroduces the entire class of injection risk that "text only" eliminates, requires a parser dependency, and needs a sanitiser whose correctness cannot be verified by inspection. Automatic URL detection gives the one affordance members actually want, performed in Go over plain text.

### D.15 Linkifying bare domains without a scheme

Detecting `example.com` and prepending `https://`.

**Rejected because:** the false-positive rate on prose is high — filenames, version strings, and abbreviations all match — and every false positive produces a broken link in a member's writing.

---

## Appendix E — Failure catalogue

Extends specification §21.5. Failures observed or predicted, with cause and resolution.

### E.1 Build and toolchain

| Symptom | Cause | Fix |
|---|---|---|
| `go test` appears to hang, no output | `modernc.org/sqlite` cold compile — several minutes, silent | Wait; or `go test -x` to see progress; subsequent builds use the cache |
| `-timeout` does not fire on the apparent hang | The timeout starts when the test *binary* runs; the delay is in the build | Distinguish with `time go build ./...` |
| `pattern template/*.mustache: no matching files found` | Embed path does not match the directory | Update the directive, `fs.ReadDir`, and `ReadFile` prefix — see L |
| `method App.X already declared` | A search/replace snippet applied twice | Delete the duplicate; verify with `grep -c "func (app \*App) X"` |
| `app.DeleteReply undefined` | Same cause: the duplicated insert consumed the function body, leaving its comment | Restore the function |

**Testing `linkify.go` without the compile wait.** It imports only `strings` and `unicode`:

```bash
mkdir -p /tmp/lk && cp linkify.go linkify_test.go /tmp/lk/
cd /tmp/lk && go mod init lk && go test -v -timeout 10s
```

Under a second, and it answers the actual question.

### E.2 Templates

| Symptom | Cause | Fix |
|---|---|---|
| Template edit has no effect | Templates embed at build time | Rebuild and restart |
| `render: template "lines" missing`, repeating every 3s | File absent at build time; the chat poller retries indefinitely | Rebuild with the file present. The startup check now prevents this class |
| Body renders empty | Section used a dotted name the implementation does not support | Use a flat top-level key — B.5 |
| Body renders as plain text, no anchors | Handler still supplies `body`; template still reads `{{body}}` | `grep -n "{{body}}" template/*.mustache` — any hit is the fault |
| Chat updates never appear | Poller lost its attributes: `hx-swap` was not `outerHTML`, so it replaced its own *content* | Ensure `hx-swap="outerHTML"` on `#poller` |
| Timestamp and handle vanished from chat lines | A search/replace on one `<span>` matched more than intended because the article is one physical line | Replace the whole `<article>`, not a fragment of it |

**Diagnosing a rendering fault:** view the raw page source (Ctrl+U), not the inspector. The inspector shows the DOM after Tailwind and HTMX have run.

| Source shows | Layer at fault |
|---|---|
| `<a href="https://...">` | Rendering is correct; the problem is visual |
| `&lt;a href=` | Anchor is being escaped — wrong tag form |
| Bare URL, no anchor | `is_link` section never entered |
| Empty body | `parts` key not reaching the template |

### E.3 Linkification

| Symptom | Cause | Fix |
|---|---|---|
| URLs inside brackets not detected | `(` absent from `isBreak`, so the scheme looked mid-word | Add `(`, `[`, `{` to `isBreak` |
| Wikipedia URLs truncate at `(` | Making `(` a break character bounds the link on both sides | Requires separate left/right boundary functions — see J.3 |
| A visible line break appears inside a body | The `{{#parts}}` block spans multiple lines inside a `whitespace-pre-wrap` container | Put the block on one line |

### E.4 CSS layout

| Symptom | Cause | Fix |
|---|---|---|
| Timestamps invisible; empty column at left | `padding-left` applied but `text-indent` did not — the two came apart | Replace with CSS grid |
| Wrapped message aligns under the timestamp | Same cause | Same fix |
| Few messages sit at the top of an empty pane | Block container stacks from the top; `scrollTop = scrollHeight` is a no-op when content is shorter than the pane | `mt-auto` on a wrapper |
| Top of long chat history unreachable | `justify-end` | Use `mt-auto` |
| Layout wrong on mobile with address bar | `vh` unit | Use `dvh` |
| Horizontal scrollbar from one long word | `1fr` grid column has a content-based minimum | `minmax(0,1fr)` |
| Navigation handle turned bold and blue | Unscoped `.who` rule also matched the header span | Resolved by the Tailwind migration: no shared class names |

### E.5 Tailwind and CSP

| Symptom | Cause | Fix |
|---|---|---|
| Page renders completely unstyled | `style-src 'self'` blocks the injected style element | Add `'unsafe-inline'` to `style-src` |
| Brief unstyled flash on load | Tailwind generates CSS at runtime | Inherent to the CDN approach — D.6 |
| Styling changes after upstream release | Unpinned CDN version | Pin the exact version in the URL |
| Console reports blocked script | Missing `script-src` entry for the CDN host | Add it |
| HTMX loads but requests blocked | `XMLHttpRequest` is governed by `connect-src`, not `form-action` | Add `connect-src 'self'` |
| Console reports blocked inline style from HTMX | HTMX 2 injects indicator styles at load | Keep the `htmx-config` meta tag |

### E.6 HTMX behaviour

| Symptom | Cause | Fix |
|---|---|---|
| Enter sends mid-composition in CJK input | Missing `evt.isComposing` check | Add it |
| Message appears twice | POST rendered the line *and* the poll fetched it | POST must return 204 + `HX-Trigger` only |
| Textarea does not grow | `chat.js` not rebuilt into the binary | Rebuild — `static/` is embedded too |

### E.7 Deployment

| Symptom | Cause | Fix |
|---|---|---|
| cloud-init fails at image pull | Registry package still private | Verify with `docker logout` then `docker pull` |
| Data gone after reboot | Docker started before `/data` mounted; container bound the boot disk | `RequiresMountsFor=/data` drop-in; check `df -h /data` |
| Volume not found on AWS | Nitro renamed `/dev/sdf` to `/dev/nvme1n1` | Mount script waits for the path then scans for an unformatted, unpartitioned disk |
| Certificate never issued | DNS wrong, port 80 closed, or Let's Encrypt rate limit from earlier failures | Verify with `dig` **before** first start |
| Every session shows the same IP | Proxy present but not in `trusted_proxies` | Set `BLOG_TRUSTED_PROXIES` |
| Geoblock blocks nobody | No range file and no trusted proxy | Startup logs a warning for exactly this |
| `UK` in block list has no effect | Not an assigned ISO code | Use `GB`; validator warns |
| No sign-in mail arrives | Port 25 blocked outbound | Use port 587 with credentials |
| `terraform destroy` fails at the volume | `prevent_destroy` | Working as designed |

---

## Appendix F — Query plans and index usage

Extends specification §5.6.

| Operation | Query shape | Index used | Cost |
|---|---|---|---|
| Session lookup | `sessions.token_hash = ?` | UNIQUE on `token_hash` | 1 seek + 2 joins |
| Login token consume | `login_tokens.token_hash = ?` | UNIQUE on `token_hash` | 1 seek |
| Member by email | `users.email = ?` | UNIQUE on `email` | 1 seek |
| Blog feed page | `is_chat = 0 ORDER BY created_at DESC` | `idx_posts_kind_time` | 1 scan of N rows |
| Channel list page | `is_chat = 1 ORDER BY last_at DESC` | `idx_posts_kind_last` | 1 scan of N rows |
| Reply count per feed row | correlated `COUNT(*)` | `idx_replies_post` | 1 seek **per row** |
| Post by id | `posts.id = ?` | PRIMARY KEY | 1 seek |
| Thread replies | `post_id = ? ORDER BY id` | `idx_replies_post` | 1 range scan |
| Chat page load | `post_id = ? ORDER BY id DESC LIMIT 100` | `idx_replies_post` | 1 range scan, 100 rows |
| Chat poll | `post_id = ? AND id > ?` | `idx_replies_post` | 1 seek, usually 0 rows |
| Chat trim | `LIMIT 1 OFFSET 500` then `id <= ?` | `idx_replies_post` | 1 seek + 1 delete |
| Session list | `user_id = ?` | `idx_sessions_user` | 1 range scan |
| Revoke others | `user_id = ? AND id <> ?` | `idx_sessions_user` | 1 range scan |
| Open invite count | `invited_by = ? AND last_login IS NULL` | `idx_users_inviter` | 1 range scan |
| Purge expired | `expires_at <= ?` | **none — full scan** | Hourly, acceptable |

**Two notes.**

The reply-count subquery on the feed is O(page size) seeks — 50 index seeks per page. Negligible on a local file, but it is the only query whose cost grows with page size in a non-obvious way. A stored `reply_count` column would be the fix, following the same reasoning as `last_at` (C.5).

`PurgeExpired` full-scans two small tables. Sessions are bounded by members × devices; tokens are purged daily.

### F.1 Linkification cost

Not a query, but on the same hot path. `Linkify` is O(n) in body length with a single forward pass. For a 16 KB post body this is microseconds. It runs once per body per render — for a 50-row feed, zero times (the feed does not render bodies); for a thread with 40 replies, 41 times; for a chat page, 100 times.

The chat poll typically linkifies zero or one message, so the 3-second poll adds no measurable cost.

### F.2 Verifying a plan

```sql
EXPLAIN QUERY PLAN
SELECT pst.id FROM posts pst WHERE pst.is_chat = 1 ORDER BY pst.last_at DESC LIMIT 50;
```

Expect `USING INDEX idx_posts_kind_last`. `USE TEMP B-TREE FOR ORDER BY` means the index is not being used and the sort is happening in memory.

---

## Appendix G — State transitions

Extends specification §6–§10.

### G.1 Member lifecycle

```
   (does not exist)
         │
         │ POST /invite  or  bootstrap
         ▼
   ┌─────────────────┐
   │ INVITED         │  enabled=1, last_login=NULL
   │                 │  counts against inviter's quota
   └────────┬────────┘
            │ opens sign-in link
            ▼
   ┌─────────────────┐
   │ ACTIVE          │  enabled=1, last_login set
   │                 │  frees the inviter's quota slot
   └────────┬────────┘
            │ UPDATE users SET enabled=0
            ▼
   ┌─────────────────┐
   │ DISABLED        │  all sessions dead on next request
   │                 │  content remains visible
   └────────┬────────┘
            │ DELETE FROM users  (not recommended)
            ▼
   (gone — cascades destroy all posts, replies, sessions)
```

An invitation link expires after 7 days but the member row remains INVITED indefinitely, permanently consuming a quota slot. A new sign-in request from `/` issues a fresh 15-minute link, so the account stays usable.

### G.2 Login token

```
CREATED ──(15 min or 7 days)──▶ EXPIRED ──(hourly purge, +1 day)──▶ deleted
   │
   └──(GET /l/{token})──▶ USED ──(hourly purge, +1 day)──▶ deleted
```

The transition to USED is atomic: the state check lives inside the `UPDATE`.

### G.3 Session

```
CREATED ──▶ ACTIVE ──(30 days)──▶ EXPIRED ──(hourly purge)──▶ deleted
              │
              ├──(POST /logout)──────────────────────▶ deleted
              ├──(revoke-others from another device)─▶ deleted
              └──(member disabled)──▶ row remains but fails the enabled check
```

Disabling a member does not delete session rows. They remain until expiry but never authenticate.

### G.4 Chat message

```
INSERTED ──▶ VISIBLE ──▶ TRIMMED (permanent) when 500 newer messages exist
                │
                └──(author or channel owner deletes)──▶ gone
```

A reader with the page open keeps seeing trimmed messages until reload, because the poll only appends.

### G.5 Body text through the render pipeline

*New in v1.1.*

```
member input
     │
     ├─ CleanText: normalise line endings, strip control chars and RuneError
     │
     ├─ ValidBody: length check (bytes)
     │
     ├─ stored verbatim in the database
     │
     ├─ Linkify: split into []Segment on read
     │
     ├─ bodyParts: convert to []map[string]any
     │
     ├─ mustache: escape each Text and URL (double-brace)
     │
     └─ browser: whitespace-pre-wrap preserves newlines
```

The database holds exactly what the member typed, minus control characters. Linkification happens at render time, so changing the scanner changes the display of existing content with no migration.

---

## Appendix H — Verification matrix

Extends specification §21.5.

### H.1 Deployment verification

| # | Layer | Command | Expected |
|---|---|---|---|
| 1 | DNS | `dig +short blog.example.com` | The instance address |
| 2 | Instance reachable | `curl -sI http://IP` | Any response |
| 3 | cloud-init | `sudo cloud-init status --wait` | `status: done` |
| 4 | Mount | `df -h /data` | 10 GB device, **not** the boot disk |
| 5 | fstab | `grep data /etc/fstab` | A UUID line with `nofail` |
| 6 | Docker ordering | `ls /etc/systemd/system/docker.service.d/` | `wait-for-data.conf` present |
| 7 | Containers | `docker compose ps` | Both `running` |
| 8 | Program | `curl localhost:8080/healthz` | `200` |
| 9 | Database | `ls -la /data/blog.db*` | Three files |
| 10 | TLS | `curl -sI https://domain/healthz` | `HTTP/2 200` |
| 11 | Root link | `docker compose logs blog \| grep -A3 'ROOT LOGIN'` | The link |
| 12 | Reboot persistence | reset instance, wait 90s, sign in | Content still present |
| 13 | Image update persistence | change tag, apply, sign in | Content still present |
| 14 | Snapshot policy | provider-specific describe | Policy attached |

Step 12 validates the entire storage arrangement. Step 13 validates the update path.

### H.2 Functional verification

| Area | Test | Expected |
|---|---|---|
| Sign-in | Unknown address | Same confirmation as known; no mail |
| Sign-in | Reuse a consumed link | "The link is used, or expired" |
| Sign-in | Link after 15 minutes | Same message |
| Keys | Sign in on a second device | Two rows; current one highlighted |
| Keys | Revoke others | Second device redirected to sign-in |
| Invite | Duplicate handle | "That address or that handle is not available" |
| Invite | Sixth open invite | Quota message |
| Posts | Delete another member's post | 403 |
| Posts | Delete a reply in your own thread | Succeeds |
| Chat | `/p/{id}` on a channel | Redirects to `/c/{id}` |
| Chat | `/c/{id}` on a post | Redirects to `/p/{id}` |
| Chat | Shift+Enter | Newline; box grows; no send |
| Chat | Enter | Sends; box clears; keeps focus |
| Chat | Second window | Message appears within 3 seconds |
| Chat | Scroll up, receive message | Position unchanged |
| Chat | Exceed the keep limit | Oldest deleted; count stays at limit |
| Links | URL in a post body | Anchor rendered, opens in new tab |
| Links | URL in a chat message | Same |
| Links | URL in a subject | **Plain text, not linked** |
| Links | `javascript:alert(1)` in a body | Escaped text, no anchor |
| Links | 200-character URL | ~60 chars shown; href complete |
| Links | Sentence ending in a URL and full stop | Full stop outside the link |
| Layout | Few chat messages | Sit at the bottom, next to the input |
| Layout | Long unbroken word in a message | No horizontal scrollbar |
| CSP | Browser console | No blocked resources |
| Styling | OS dark mode, reload | Dark palette |
| No-JS | Disable JavaScript, send a message | Works via redirect |
| Geoblock | Request from a blocked country | 403, plain text |
| Geoblock | `/healthz` from a blocked country | 200 |

---

## Appendix I — HTTP status codes in use

Extends specification §13.3.

| Code | Meaning here | Emitted by |
|---|---|---|
| 200 | Page rendered; or HTMX delete succeeded (empty body) | `Render`, delete handlers |
| 204 | Chat message accepted; poller will fetch it | `CreateChatLineHandler` under HTMX |
| 303 | Post-Redirect-Get after any state change | All non-HTMX POST handlers |
| 400 | Malformed request, or wrong kind (post vs channel) | `parseForm` failure, kind guards |
| 403 | Geoblocked; failed CSRF; not the owner | `GeoBlock`, `CheckCSRF`, ownership checks |
| 404 | Post, channel, or reply does not exist | Id parse failure, `ErrNotFound` |
| 413 | Request body over 32 KB | `http.MaxBytesReader` |
| 429 | Rate limit on sign-in link opens | `ConsumeLink` |
| 500 | Template or database failure | `Render`, `Fail` |
| 503 | Health check with database unreachable | `/healthz` |

403 covers three distinct conditions deliberately. Ownership failures return 403 rather than 404 because the resource exists; the member has no right to it. The `DeleteReply` query cannot distinguish "not yours" from "does not exist" — both yield zero affected rows — and returns 403, the conservative choice.

---

## Appendix J — Linkification test vectors

Extends specification §11. Reference cases for `Linkify`.

### J.1 Detection

| Input | Segments | Notes |
|---|---|---|
| `see https://example.com/page` | text, link | Text before preserved |
| `https://example.com` | link | Whole body is one link |
| `a https://x.com b https://y.com c` | text, link, text, link, text | Multiple links |
| `plain text only` | text | One segment |
| `` (empty) | none | Empty slice |
| `http://example.com` | link | Both schemes recognised |
| `HTTPS://EXAMPLE.COM` | text | **Case-sensitive; not detected** |
| `xhttps://example.com` | text | Scheme mid-word |
| `https://` | text | Nothing after `://` |
| `ftp://example.com` | text | Scheme not recognised |
| `mailto:someone@example.com` | text | Not recognised |
| `example.com` | text | Bare domain not linked — D.15 |

### J.2 Boundaries

| Input | Link portion | Notes |
|---|---|---|
| `See https://example.com.` | `https://example.com` | Trailing full stop excluded |
| `Try https://example.com, then...` | `https://example.com` | Trailing comma excluded |
| `Is it https://example.com?` | `https://example.com` | Trailing question mark excluded |
| `https://example.com/a?b=1&c=2` | whole | Query string retained |
| `https://example.com/a#frag` | whole | Fragment retained |
| `line one\nhttps://x.com\nline three` | `https://x.com` | Newline bounds the link |
| `"https://example.com"` | `https://example.com` | Quotes excluded |

### J.3 Brackets — the unresolved case

| Input | Current behaviour | Ideal |
|---|---|---|
| `(https://example.com)` | Detected; closing bracket excluded | Same |
| `https://en.wikipedia.org/wiki/Foo_(bar)` | **Truncates at `(`** | Whole URL |

Adding `(` to `isBreak` was necessary so a URL immediately after an opening bracket is detected at all. The side effect is that `(` also terminates a URL, which truncates Wikipedia-style addresses. The bracket-balancing logic in `trimTrailing` becomes unreachable as a result.

The fix is two boundary functions — `isStartBreak` for the left edge (includes `(`, `[`, `{`) and `isBreak` for the right edge (excludes them, relying on `trimTrailing` to balance). Not implemented; recorded so the trade-off is visible.

### J.4 Security

| Input | Result | Guarantee |
|---|---|---|
| `javascript:alert(1)` | Escaped text | Scheme filter |
| `data:text/html,<script>` | Escaped text | Scheme filter |
| `https://x.com" onmouseover="alert(1)` | Link stops at the quote; remainder escaped | `isBreak` includes `"` |
| `<script>alert(1)</script>` | Escaped text | Mustache double-brace |
| `https://x.com/<script>` | Link stops at `<` | `isBreak` includes `<` |

### J.5 Display shortening

| Input length | Display | Href |
|---|---|---|
| ≤60 chars | Full URL including scheme | Full |
| 61–~68 chars | Scheme stripped, rest shown | Full |
| Longer | Scheme stripped, truncated at 59 + `…` | Full |

The href always carries the complete address. The displayed text is never a valid URL when shortened, which is intentional — it signals truncation.

### J.6 Minimal test harness

```go
// linkify_test.go — package main, but see E.1 for the fast path
package main

import "testing"

func TestLinkify(t *testing.T) {
	cases := []string{
		"Testing the URLs again: https://github.com/ghowland/blogchat  Here.",
		"See https://example.com.",
		"javascript:alert(1)",
		"no urls here",
	}
	for _, body := range cases {
		t.Logf("--- %q", body)
		for idx, seg := range Linkify(body) {
			t.Logf("  %d: is_link=%v text=%q url=%q", idx, seg.IsLink, seg.Text, seg.URL)
		}
	}
}
```

---

## Appendix K — Tailwind class inventory

Extends specification §14.6–§14.7. The recurring patterns, so new templates stay consistent.

### K.1 Page structure

| Element | Classes |
|---|---|
| `body` | `min-h-screen bg-white text-zinc-900 antialiased dark:bg-zinc-950 dark:text-zinc-100` |
| Header bar | `border-b border-zinc-200 dark:border-zinc-800` |
| Content container | `mx-auto max-w-3xl px-4 py-6` |
| Footer | `mx-auto mt-8 flex max-w-3xl gap-4 border-t px-4 py-4 text-sm text-zinc-500` |

### K.2 Repeated components

| Pattern | Classes |
|---|---|
| Button | `rounded-md border border-zinc-300 bg-zinc-100 px-4 py-2 text-sm font-medium hover:bg-zinc-200 focus:ring-2 focus:ring-blue-500 focus:outline-none dark:border-zinc-700 dark:bg-zinc-800 dark:hover:bg-zinc-700` |
| Danger button | `rounded-md border border-red-300 px-3 py-1.5 text-sm font-medium text-red-700 hover:bg-red-50 dark:border-red-900 dark:text-red-400 dark:hover:bg-red-950` |
| Input / textarea | `w-full rounded-md border border-zinc-300 bg-white px-3 py-2 focus:border-blue-500 focus:ring-1 focus:ring-blue-500 focus:outline-none dark:border-zinc-700 dark:bg-zinc-950` |
| Card | `rounded-lg border border-zinc-200 bg-zinc-50 p-4 dark:border-zinc-800 dark:bg-zinc-900` |
| Link | `text-blue-600 hover:underline dark:text-blue-400` |
| Body link (in prose) | `underline decoration-blue-300 underline-offset-2 hover:decoration-blue-600 dark:decoration-blue-700` |
| Notice | `rounded-md border border-green-200 bg-green-50 px-4 py-3 text-sm text-green-900 dark:border-green-900 dark:bg-green-950 dark:text-green-200` |
| Error | `rounded-md border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-800 dark:border-red-900 dark:bg-red-950 dark:text-red-300` |
| List separator | `divide-y divide-zinc-200 dark:divide-zinc-800` |
| Body text container | `whitespace-pre-wrap break-words` |

The in-prose link class omits `text-blue-600` deliberately: the chat handle is already blue, and two blue elements on one line compete. The underline distinguishes it.

### K.3 Chat-specific

| Element | Classes | Constraint |
|---|---|---|
| Page column | `flex h-[calc(100dvh-8rem)] flex-col` | `dvh`, never `vh` |
| Message pane | `flex flex-1 flex-col overflow-y-auto overscroll-contain py-1` | — |
| Line wrapper | `mt-auto` | Never `justify-end` |
| Line | `group grid grid-cols-[3.5rem_6rem_minmax(0,1fr)_auto] items-baseline gap-x-2 px-1 py-0.5 text-sm` | `minmax(0,1fr)`, never `1fr` |
| Time cell | `text-xs tabular-nums text-zinc-400 dark:text-zinc-500` | — |
| Handle cell | `truncate text-right font-semibold text-blue-600 dark:text-blue-400` | — |
| Text cell | `whitespace-pre-wrap break-words` | — |
| Delete control | `invisible px-1 leading-none text-red-500 group-hover:visible` | Needs `group` on the line |
| Input row | `flex items-end gap-2 border-t pt-3 pb-4` | — |
| Message box | `max-h-36 min-h-10 flex-1 resize-none overflow-y-auto` | `chat.js` overrides height inline |

### K.4 Theme block

```html
<style type="text/tailwindcss">
  @theme {
    --height-chatpane: calc(100dvh - 8rem);
  }
</style>
```

Tailwind v4 reads theme values from CSS rather than a config file, which is what makes the no-build-step approach viable.

---

## Appendix L — Embed path and build-time coupling

Extends specification §18.1 and §21.2. This is where most development-time confusion originates.

### L.1 What is embedded

| Directive | File | Covers | Renaming requires |
|---|---|---|---|
| `//go:embed template/*.mustache` | `views.go` | 15 template files | Directive, `fs.ReadDir` arg, `ReadFile` prefix, error message |
| `//go:embed static` | `routes.go` | `chat.js` | Directive, `fs.Sub` arg |

Both paths are relative to `code/`, the directory containing the `.go` files.

### L.2 What is not embedded

| Asset | Source | Consequence |
|---|---|---|
| HTMX | CDN | Network required at page load |
| Tailwind | CDN | Network required at page load |
| `config.json` | Filesystem or environment | Read at startup |
| Geo range CSV | Filesystem | Read at startup |

### L.3 The rebuild rule

**Any change to a file under `template/` or `static/` requires a rebuild and restart.** The running binary holds a snapshot from build time.

This produces two distinct confusions:

**Change appears to do nothing.** The template edit is correct but the binary is old. The fix is `go build && ./blog`.

**Build fails with `no matching files found`.** The directory was renamed but a directive was not. `go test` compiles the package first, so this presents as a test hang rather than a build error if the person is not watching for it.

### L.4 Minimum contents

`//go:embed static` fails to compile if the directory is empty. `chat.js` satisfies this. If `chat.js` were ever removed, the directive would need removing too, along with the `/static/` route.

### L.5 Verification

```bash
grep -rn "go:embed" code/
ls code/template/ code/static/
go build -o /dev/null ./code
```

To confirm a specific template made it into the binary:

```bash
strings blog | grep -c 'hx-get="/c/'
```

Non-zero means `lines.mustache` (or `chat.mustache`) is present.

---

## Appendix M — Extension points

Where a future change would go, and what it would disturb. Extends specification §23.

| Feature | Where | Difficulty | Disturbs |
|---|---|---|---|
| Edit posts | New route + handler; `updated_at` already exists | Low | Nothing; the column is reserved |
| Full-text search | SQLite FTS5 virtual table + trigger | Medium | New migration; driver must include FTS5 |
| Email digest | New goroutine in the hourly loop | Medium | Needs a `last_notified` column |
| Handle rename | Handler + uniqueness check | Low | Breaks the "permanent handle" property |
| Admin interface | New `is_admin` column + routes | Medium | New authorisation layer throughout |
| Audit log | New table + calls in delete handlers | Low | Storage growth |
| Per-site channel quota | Count channels in `CreateChannelHandler` | Low | Nothing |
| Global trim pass | Add to `purgeLoop` | Low | Makes `chat_keep` reductions retroactive |
| IPv6 geoblocking | Second sorted array of `uint128` ranges | Medium | Parallel code path in `geo.go` |
| `mailto:` links | Add scheme to `findScheme`; extend `isBreak` | Low | Widens the href surface — revisit P.5 |
| Wikipedia-safe brackets | Split `isBreak` into start/end variants | Low | `linkify.go` only — J.3 |
| Per-context link length | Parameter on `bodyParts` | Low | Two call sites |
| Self-hosted HTMX/Tailwind | Move files to `static/`; edit CSP and src | Low | Loses SRI; gains offline capability |
| Tailwind CLI build | Add build step; tighten `style-src` | Medium | Build pipeline, Dockerfile — D.6 |
| Multiple instances | Replace `db.go` with a network database | **High** | Invalidates C.1, C.2, §20.3 entirely |
| Read receipts | New table keyed on member+channel | Medium | Write amplification on the single connection |

The last two rows are the boundary of the design. Everything above is an addition; those are a different system.

---

## Appendix N — Cost model

Extends specification §20. Approximate monthly figures; verify current prices.

### N.1 Infrastructure

| Component | GCP | AWS | DigitalOcean | Vultr |
|---|---|---|---|---|
| Instance | $0 (free tier) – $7 | ~$12 (t4g.small) | $6 | $6 |
| Data volume 10 GB | ~$1 | ~$1 | $1 | $1 |
| Static address | included | included | included | included |
| Snapshots (28 daily) | <$0.10 | <$0.10 | ~$0.05/GB | ~$0.05/GB |
| Egress (text-only) | negligible | negligible | included | included |
| **Total** | **$1–8** | **~$13** | **~$7** | **~$7** |

A cloud load balancer would add $16–18, more than doubling the cost. This is why C.40 chose Caddy.

### N.2 Storage growth

| Item | Approximate size |
|---|---|
| Member row | ~200 bytes |
| Session row | ~250 bytes |
| Post (16 KB body) | up to 16 KB |
| Typical post | ~1 KB |
| Chat message | ~150 bytes |
| Full channel (500 messages) | ~75 KB |
| 50 members, 1,000 posts, 20 channels | ~3 MB |

The 10 GB volume is sized for filesystem and snapshot mechanics, not for data.

### N.3 Snapshot cost behaviour

The first snapshot bills for used blocks (~1 GB including filesystem overhead). Subsequent ones bill only for changed blocks — a few megabytes daily. Twenty-eight retained snapshots cost close to the first plus a small increment, not 28× the volume.

### N.4 Bandwidth per page

| Asset | Size | Cached? |
|---|---|---|
| HTML page | 3–15 KB | No |
| Tailwind browser build | ~100 KB | Browser cache, CDN |
| HTMX | ~50 KB | Browser cache, CDN |
| `chat.js` | ~1.5 KB | Browser cache, own origin |
| Chat poll response | ~200 bytes typical | No |

The two CDN assets do not consume your egress at all — they come from the CDN. Your egress is HTML plus poll responses. A member with a chat page open for an hour costs about 240 KB in poll responses.

---

## Appendix O — Dependency and version matrix

Extends specification §18.3.

### O.1 Build-time

| Item | Version | Why this one |
|---|---|---|
| Go | 1.24 | `ServeMux` method patterns (1.22+); `crypto/rand` no longer returns an error (1.24) |
| `modernc.org/sqlite` | ~1.34 | Pure Go — enables `CGO_ENABLED=0` and a static binary |
| `github.com/cbroglie/mustache` | ~1.4 | Supports `StaticProvider`, `ParseStringPartials`, `FRender`, dotted variable names |

**Verify three names on first build:** `StaticProvider`, `ParseStringPartials`, `Template.FRender`. Mustache packages differ. If they differ, only `views.go` changes.

**Verify dotted-name support** for variable tags (`{{post.id}}`). Section tags already avoid dotted names by design — B.5.

### O.2 Runtime, browser

| Item | Version | Delivery | Failure mode if unreachable |
|---|---|---|---|
| Tailwind browser | 4.1.11 | CDN + SRI | Page renders unstyled but fully functional |
| HTMX | 2.0.10 | CDN + SRI | Chat loses live updates and Enter-to-send; forms still work |
| `chat.js` | — | Embedded, own origin | Always available |

Both CDN assets degrade gracefully. Neither is required for correctness — only for appearance and convenience.

**Pin both exact versions.** An unpinned URL means the site restyles or rebehaves itself when upstream publishes.

Obtain an integrity hash:

```bash
curl -sL <url> | openssl dgst -sha384 -binary | openssl base64 -A
```

### O.3 Runtime, server

| Item | Version | Notes |
|---|---|---|
| Caddy | 2-alpine | Separate container |
| Base image | distroless static-debian12:nonroot | Supplies CA bundle and tzdata |

### O.4 Deployment tooling

| Item | Version |
|---|---|
| Terraform | ≥1.5 |
| `hashicorp/google` | ~6.0 |
| `hashicorp/aws` | ~5.0 |
| `digitalocean/digitalocean` | ~2.0 |
| `vultr/vultr` | ~2.0 |

### O.5 Deliberately absent

No web framework, no ORM, no router library, no logging library, no configuration library, no CSS build step, no JavaScript bundler, no transpiler, no Node.js at any stage.

---

## Appendix P — Threat table

Extends specification §16.

| # | Threat | Mitigation | Residual risk |
|---|---|---|---|
| 1 | Password database theft | No passwords exist | — |
| 2 | Session token theft from database | Only SHA-256 hashes stored | — |
| 3 | Session theft via script | `HttpOnly` cookie; CSP `script-src` limited | XSS via a CSP gap |
| 4 | Cross-site request forgery | HMAC token in every form + `SameSite=Lax` | — |
| 5 | SQL injection | Parameters everywhere; one concatenation from local constants | — |
| 6 | HTML injection in bodies | Mustache double-brace; triple-brace banned | See P.3 |
| 7 | **Link injection via href** | Scheme filter + escaping + `base-uri 'none'` | See P.5 |
| 8 | Membership enumeration via sign-in | Identical response for unknown addresses | — |
| 9 | Membership enumeration via invite | Identical message for both duplicate types | — |
| 10 | Mail flooding | 5 per 15 min per address and per IP | — |
| 11 | Mail header injection | CR/LF stripped, values truncated | — |
| 12 | Brute-force of a sign-in link | 256-bit token, 15-minute window, single use | — |
| 13 | Content spam | Per-member rate limits on every create action | A determined member can fill the DB |
| 14 | **Style injection** | — | **`unsafe-inline` permitted — P.4** |
| 15 | **Intercepted sign-in link** | Keys page + revoke-others | **Unmitigated at the protocol level** |
| 16 | **Compromised mailbox** | Same | **Unmitigated by design** |
| 17 | **Geographic bypass** | None | **Trivially bypassed by VPN** |
| 18 | **Host compromise** | Distroless image; non-root user | **Root on host reads everything** |
| 19 | **Malicious member** | Ownership rules limit deletion scope | **No audit trail** |
| 20 | CDN compromise | SRI hash on both assets | Availability dependency remains |
| 21 | Denial of service | Rate limits on writes only | No read-side rate limit |

### P.1 Bearer-credential property

Threats 15 and 16 are the fundamental limit of passwordless email authentication. This is inherent, not a defect. The keys page exists specifically as the detection and recovery mechanism, which is why sign-in redirects there (C.12).

### P.2 What "not a security control" means for the geoblock

It reduces unwanted traffic volume. It must not be relied upon for legal compliance, access control, or data-residency claims.

### P.3 The Mustache escaping boundary

Mustache escapes for **HTML text context only** — not attribute, URL, CSS, or script context. Therefore:

- Member-supplied values may appear as element text. ✓
- Member-supplied values must **never** appear in `src`, `style`, `on*` attributes, or `<script>` blocks. ✗
- The one `href` exception is safe only because of the scheme filter — see P.5.

`html/template` would enforce this by context; Mustache does not. This is the cost of C.17.

### P.4 The `unsafe-inline` concession

*New in v1.1.* Permitting inline styles means an attacker who achieves HTML injection can also inject styles — enabling clickjacking-style overlays and content obscuring, though not script execution.

The practical exposure is low because the injection surface is small: every member value is escaped, and no member value reaches an attribute except the filtered `href`. But this is a genuine reduction from v1.0's posture, accepted as the cost of CDN-delivered Tailwind. D.6 records the alternative.

### P.5 The href surface

*New in v1.1.* This is the only path where member input reaches an HTML attribute. Three independent layers:

1. **Scheme filter.** `findScheme` matches only `http://` and `https://`. Nothing else produces a `Segment` with `IsLink` true.
2. **Escaping.** `{{url}}` is a double-brace tag, so a quote in the address cannot break out of the attribute.
3. **Boundary characters.** `isBreak` includes `"`, `'`, `<`, `>`, so an address terminates before any character that could alter the markup.

Any extension that adds a scheme (M: `mailto:` links) must re-examine this appendix. Adding a scheme whose handler can execute — or a scheme-relative `//host` form — would defeat layer 1.

---

## Appendix Q — Data lifecycle

Extends specification §5 and Appendix G.

| Data | Created | Retained | Deleted by | Recoverable? |
|---|---|---|---|---|
| Member row | Invite or bootstrap | Indefinitely | Manual SQL only | From snapshot |
| Email address | With the member | Life of the member | With the member | From snapshot |
| Login token | Sign-in request | 15 min or 7 days | Hourly purge, +1 day | No (and no value) |
| Session | Link consumption | 30 days | Purge, logout, revoke, expiry | No |
| Session IP/agent | With the session | With the session | With the session | No |
| Post | Member action | Indefinitely | Owner, or member cascade | From snapshot |
| Blog reply | Member action | Indefinitely | Author, thread owner, or cascade | From snapshot |
| Channel | Member action | Indefinitely | Owner, or member cascade | From snapshot |
| **Chat message** | Member action | **500 newest per channel** | **Trim, author, owner, cascade** | **From snapshot only** |
| Link segments | Render time | Not stored | — | Recomputed each render |
| Request log | Every request | Platform retention | Platform | Platform |
| Snapshot | Daily 03:00 UTC | 28 days | Retention policy | — |

### Q.1 Cascade consequences

Deleting a `users` row cascades to `login_tokens`, `sessions`, `posts`, and `replies` — and posts cascade further to their replies. **A single member deletion can therefore remove content written by other members** (their replies inside the deleted member's threads).

This is why §21.3 states that `enabled = 0` is the correct removal mechanism.

### Q.2 The only permanent loss

Chat trimming is the one place where the system destroys data during normal operation, with no confirmation and no undo. Both the channel list and the channel page state the retention limit for this reason.

### Q.3 Linkification is not persisted

*New in v1.1.* Segments are computed at render time and never stored. Two consequences:

- Changing the scanner changes the display of existing content with no migration.
- A body stored before linkification existed renders with links immediately after an upgrade.

The database always holds exactly what the member typed, minus control characters.

---

## Appendix R — Error message inventory

Every message a member can see, its trigger, and its status code. Messages are deliberately vague where vagueness prevents information disclosure.

| Message | Trigger | Code | Deliberately vague? |
|---|---|---|---|
| "The email address is not valid" | Failed structural check | 200 (re-render) | No |
| "Too many requests, wait 15 minutes" | Sign-in rate limit | 200 (re-render) | No |
| "If that address belongs to a member, a sign-in link is now on the way." | Any sign-in submission | 200 | **Yes** — hides membership |
| "The link is used, or expired. Request a new link." | Bad, used, or expired token; or disabled member | 200 | **Yes** — does not distinguish |
| "The form is expired, try again" | CSRF mismatch | 403 | No |
| "The subject is empty" / "is too long" | Validation | 303 + query | No |
| "The message is empty" / "is too long" | Validation | 303 + query | No |
| "The topic is too long" | Validation | 303 + query | No |
| "The handle must have 2 to 24 characters" | Validation | 200 | No |
| "The handle accepts letters, digits, and the signs _ - ." | Validation | 200 | No |
| "That address or that handle is not available" | UNIQUE violation on either column | 200 | **Yes** — does not distinguish |
| "Your open invites are at the limit" | Quota reached | 200 | No |
| "The post does not exist" | Bad id or `ErrNotFound` | 404 | No |
| "The channel does not exist" | Bad id or `ErrNotFound` | 404 | No |
| "You do not own this post" | Zero rows on delete | 403 | No |
| "You cannot delete this reply" | Zero rows on delete | 403 | Partly — also covers non-existence |
| "That is a channel, not a post" | Kind guard on reply POST | 400 | No |
| "That is a post, not a channel" | Kind guard on message POST | 400 | No |
| "Too many posts / replies / channels / messages, wait one minute" | Per-action rate limit | 303 + query | No |
| "Not available in your region." | Geoblock | 403 | No |
| "The request is too large" | Body over limit | 400 | No |
| "Server error" | Template or database failure | 500 | **Yes** — details go to log only |

### R.1 Principle

1. **A message shown to an unauthenticated visitor must not reveal whether an account exists.**
2. **A message shown after an internal failure must contain no internal detail.**

### R.2 Startup messages (operator-facing)

These appear on standard output, not to members. Different rule applies: they must be as specific as possible.

| Message | Cause |
|---|---|
| `site_url is required, because the login link uses it` | Missing mandatory config |
| `mail_from is required` | Missing mandatory config |
| `blocked_countries[N]: "XX" is not a two-letter code` | Malformed country code |
| `config warning: UK is not an ISO country code, use GB` | Non-fatal warning |
| `template X.mustache is missing from template/` | Template not embedded |
| `geo warning: a block list exists, but there is no range file and no trusted proxy` | Geoblock will do nothing |
| `the database has no members: start once with -seed-email and -seed-handle` | Bootstrap needed |
| `migration N: <error>` | Schema failure |

---

## Appendix S — Migration record, v1.0 to v1.1

What changed, in what order, and what it disturbed. Retained so the reasoning is not lost.

### S.1 Change summary

| # | Change | Files touched | Breaking? |
|---|---|---|---|
| 1 | Source moved to `code/` | All Go files (path only) | No |
| 2 | `tmpl/` renamed to `template/` | `views.go` (3 references) | **Build failure if incomplete** |
| 3 | Hand-written CSS deleted | `static/style.css` removed | Yes — all templates |
| 4 | Tailwind from CDN | `header.mustache`, `views.go` (CSP) | Yes — all templates |
| 5 | All 17 templates restyled | `template/*.mustache` | Yes |
| 6 | URL linkification, bodies | `linkify.go` (new), `handlers.go`, `post.mustache` | No |
| 7 | URL linkification, chat | `handlers.go` (`chatItem`), `line.mustache` | No |

Changes 1–2 are mechanical. Changes 3–5 are a single unit and cannot be partially applied. Changes 6–7 are additive.

### S.2 Order dependency

Change 4 (CSP) must land before change 5 (templates), or every page renders unstyled with a console error. Change 6 must land before change 7, because change 7 reuses `bodyParts`.

### S.3 Database impact

**None.** No migration was added. The schema is unchanged from v1.0. Linkification operates entirely at render time (Q.3).

### S.4 Context key changes

| Template | v1.0 | v1.1 |
|---|---|---|
| `post` | `post.body` | `post_parts[]` (flat key — B.5) |
| `post` replies | `body` | `parts[]` |
| `line` | `body` | `parts[]` |

`post.body` remains in the context map during the transition to avoid breaking a template mid-edit, but nothing reads it.

### S.5 Defects encountered and their lessons

| Defect | Lesson |
|---|---|
| Duplicated functions from a twice-applied snippet | Anchor-based replacements are not idempotent; verify with `grep -c` |
| Test appeared to hang | The delay was the SQLite cold compile, not the code; `-timeout` does not cover build time |
| Build failure after directory rename | `go:embed` paths are string literals — a rename is not automatic |
| Chat timestamp and handle deleted | The `line.mustache` article is one physical line; partial replacements are hazardous |
| URLs not linked despite correct scanner | Templates embed at build time; the edit had not been compiled in |

Four of the five are variants of one root cause: **the gap between editing a file and that file taking effect.** Appendix L exists because of this.

---

*End of appendices.*
